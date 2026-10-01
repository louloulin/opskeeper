package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/ports"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/pluginmanifest"
)

// The node's plugin store: what a package has to survive to become
// something this node's agent can call.
//
// The order of the checks below is the whole design, and it is not
// negotiable. A package arrives as bytes from a URL, and everything the
// bytes say about themselves is a claim by the bytes. So:
//
//	1. the transport digest is checked before the archive is opened, so a
//	   corrupt or substituted body never reaches the extractor at all;
//	2. the bytes are extracted into a staging directory that is not the
//	   package directory, so nothing a package contains is reachable from
//	   the active set until the last step;
//	3. the review runs in the node's own fixed order — signature, then
//	   manifest, then this node's policy — so a package that lies about
//	   its capabilities is refused by the signature step rather than by a
//	   later check that would have believed it;
//	4. the manifest's own name and version must agree with what the
//	   control plane asked for, so a mirror that serves two different
//	   bodies for one version cannot install under either name;
//	5. only then is the staging directory moved into place and the agent's
//	   package list rewritten atomically.
//
// Any failure at any step leaves the previous package set exactly as it
// was. That is not tidiness: a node that has lost a tool it had an hour
// ago produces a model that confidently reports a capability it does not
// have, and nothing in the transcript distinguishes that from a bug.

// maxPluginBundleBytes caps a plugin tarball.
//
// A plugin is Go sources, Markdown and manifests — the largest shipped
// package is a few hundred kilobytes. A megabyte is generous by two
// orders of magnitude, and the bound is here because this is a file size
// chosen by whoever controls the URL, read by a process on a node that
// has a disk rather than a memory budget.
const maxPluginBundleBytes = 4 << 20

// pluginDownloadTimeout bounds one fetch.
//
// Short, and much shorter than the edge upgrade channel's: a plugin is
// small and the release server is on the same network, so a fetch that is
// taking minutes is a fetch that has failed in a way a timeout will
// describe better.
const pluginDownloadTimeout = 2 * time.Minute

// pluginFetcher downloads a package body. It is a field so the tests can
// supply an archive without a server, and it is deliberately the only
// thing about the transport this file knows.
type pluginFetcher func(ctx context.Context, url string) ([]byte, error)

// pluginStore is the node's implementation of ports.PluginInstaller.
type pluginStore struct {
	// pkgDir holds one directory per installed package, named
	// "<name>-<version>". It is a directory rather than a database
	// because the packages themselves are directories, and a store that
	// could describe a package but not produce its files would be a
	// second thing that has to stay in sync.
	pkgDir string
	// workDir is the agent's working directory, which is where
	// writeAgentSettings puts the package list. It is separate from
	// pkgDir because one is the content and the other is a pointer at it.
	workDir string
	// base is the set of packages admitted at boot from the operator's
	// configured bundle, which the store does not own and does not
	// remove.
	//
	// It is here rather than absent because the settings file is written
	// from scratch on every republish. A store that wrote only what it
	// installed would drop the boot bundle the first time anybody
	// installed a plugin at run time — and the symptom would be a node
	// whose agent has quietly lost the read-only package it has been
	// running with since it booted. A base package that stops passing
	// review is a boot error, so it fails the republish rather than being
	// dropped: the operator put it there on purpose.
	base []string
	// trust and policy are the node's own review inputs. The control
	// plane supplies neither.
	trust  *pluginmanifest.TrustStore
	policy pluginmanifest.Policy
	// fetch is the transport. Defaults to an http one.
	fetch pluginFetcher
	log   *slog.Logger

	// mu serialises installs. Two concurrent installs would race on the
	// staging directory name and on the settings file, and the loser of
	// that race would be a node with a package set neither request asked
	// for.
	mu sync.Mutex
}

// reviewPolicy returns the policy this node reviews against.
//
// The unsigned escape hatch is derived from the trust store's own
// presence, exactly as it is at boot, so a package cannot be admitted
// mid-life under a laxer rule than the one the node started with. A node
// with no trust store has not been provisioned and runs unsigned packages
// with a warning; a node with an unreadable one cannot verify anything and
// therefore admits nothing that needs verifying.
func (s *pluginStore) reviewPolicy() (pluginmanifest.Policy, error) {
	trust := s.trust
	if trust == nil {
		trust = pluginmanifest.NewTrustStore()
	}
	pol := s.policy
	pol.AllowUnsigned = len(trust.KeyIDs()) == 0
	if pol.AllowUnsigned {
		if err := trust.LoadError(); err != nil {
			return pluginmanifest.Policy{}, fmt.Errorf("trust store: %w", err)
		}
	}
	return pol, nil
}

// defaultPluginStoreDir is where runtime-installed packages live.
//
// It is not the bundle directory. The bundle is provisioned by the
// operator, is read-only in every sane deployment, and is what the node
// boots with; putting a downloaded package inside it would mean a
// manifest the operator wrote could name a directory this node fills in
// on its own, which is a much larger thing to be true than it sounds.
const defaultPluginStoreDir = "/var/lib/opskeeper-edge/plugins"

// newPluginStore builds the node's store from its configuration.
//
// The trust store and the policy are loaded here rather than passed in,
// because they are the operator's configuration read from the operator's
// environment — and because the one place that must not be able to
// disagree is the set of review inputs. A store built with a policy the
// boot path did not use would admit under rules the boot path never
// applied, and the difference would only show up as a package that
// installed after a restart and not before.
func newPluginStore(cfg nodeAgentConfig, log *slog.Logger) (*pluginStore, error) {
	policy, err := nodePluginPolicy()
	if err != nil {
		return nil, fmt.Errorf("edge plugin policy: %w", err)
	}
	dir := strings.TrimSpace(os.Getenv("OPSKEEPER_EDGE_PLUGIN_STORE_DIR"))
	if dir == "" {
		dir = defaultPluginStoreDir
	}
	// Admit the boot bundle so the store republishes it rather than
	// replacing it. A bundle that no longer passes is a boot error and
	// the caller already has one from admitPackages; a store that
	// reported it a second time would be noise, and a store that ignored
	// it would write a settings file the node did not boot with.
	base := cfg.Packages
	if err := os.MkdirAll(dir, 0o750); err != nil {
		// Not fatal. A node whose store directory cannot be created can
		// still run everything it booted with; it just cannot take a new
		// package, and it says so per request rather than refusing to
		// start.
		log.Warn("plugin store directory is not usable; the node will run its boot packages only",
			slog.String("dir", dir), slog.Any("err", err))
	}
	return &pluginStore{
		pkgDir:  dir,
		workDir: cfg.Cwd,
		base:    base,
		trust:   loadTrustStore(),
		policy:  policy,
		log:     log,
	}, nil
}

// Install stages, verifies, reviews and activates one package.
func (s *pluginStore) Install(ctx context.Context, spec ports.PluginSpec) ports.PluginState {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateSpec(spec); err != nil {
		return s.refuse(spec, err.Error())
	}

	pol, err := s.reviewPolicy()
	if err != nil {
		return s.fail(spec, err)
	}

	// 1. Fetch, and check the transport digest before anything parses it.
	body, err := s.download(ctx, spec)
	if err != nil {
		return s.fail(spec, err)
	}
	if err := verifyDigest(body, spec.SHA256); err != nil {
		return s.refuse(spec, err.Error())
	}

	// 2. Extract into a staging directory that is not the package
	// directory. Nothing a package contains is reachable from the active
	// set until step 5.
	if err := os.MkdirAll(s.pkgDir, 0o750); err != nil {
		return s.fail(spec, fmt.Errorf("create the package directory: %w", err))
	}
	stage, err := os.MkdirTemp(s.pkgDir, ".staging-*")
	if err != nil {
		return s.fail(spec, fmt.Errorf("stage: %w", err))
	}
	// Every failure from here on removes the staging directory. It is
	// deferred rather than repeated at each return so a new early return
	// cannot forget it, and a staged-but-unreviewed package left on disk
	// is a directory a later boot might pick up.
	defer func() { _ = os.RemoveAll(stage) }()

	if err := extractPluginTarGz(body, stage); err != nil {
		// A malformed archive is a refusal, not a failure. Nothing about
		// this node was the problem, and a manager that reads it as a
		// transport fault will retry the same bytes across the fleet.
		return s.refuse(spec, fmt.Sprintf("the archive could not be unpacked: %v", err))
	}

	// 3. The node's own review, in its own order. This is the same
	// Review the boot path uses, deliberately: a package installed at
	// runtime must not face a weaker gate than one installed by hand.
	root, err := packageRootWithin(stage)
	if err != nil {
		return s.refuse(spec, err.Error())
	}
	decision := pluginmanifest.Review(root, s.trust, pol)
	if !decision.Allowed {
		return s.refuse(spec, decision.String())
	}
	if s.log != nil {
		s.log.Info("plugin package reviewed",
			"plugin", spec.Name, "version", spec.Version, "verdict", decision.String())
	}

	// 4. The manifest must agree with the request. A release served under
	// the wrong version cannot be rolled back to, and a manager that
	// mislabels what it shipped has a bug this is the cheapest place to
	// catch it.
	p, err := pluginmanifest.Load(root)
	if err != nil {
		return s.refuse(spec, fmt.Sprintf("the package was signed and reviewed but no longer loads: %v", err))
	}
	if p.Name() != spec.Name {
		return s.refuse(spec, fmt.Sprintf(
			"the package declares itself %q but was offered as %q; a release served under the wrong "+
				"name cannot be rolled back to, so it is not installed", p.Name(), spec.Name))
	}
	if got := p.Manifest.Metadata.Version; got != spec.Version {
		return s.refuse(spec, fmt.Sprintf(
			"the package declares version %q but was offered as %q; refusing rather than installing "+
				"something whose version is a guess", got, spec.Version))
	}
	if !p.RunsOn("edge") {
		return s.refuse(spec, fmt.Sprintf(
			"the package declares targets %v and does not run on an edge", p.Manifest.Spec.Targets))
	}
	digest, err := pluginmanifest.TreeDigest(root)
	if err != nil {
		return s.refuse(spec, fmt.Sprintf("digest the reviewed tree: %v", err))
	}

	// 5. Move into place, then republish the package list. The move is
	// the last thing that can fail destructively, so it is the last
	// thing done.
	//
	// What was there before is read first, while the old version is
	// still on disk. After the rename the store can no longer tell — the
	// directory name carries the new version, and the old directory is
	// not one this store looks at — and the manager cannot work it out
	// either, because the only list the node reports afterwards has the
	// new version in it. This is the one moment the answer exists.
	replaced := s.replacedBy(spec)

	if err := s.activate(spec, root); err != nil {
		return s.fail(spec, err)
	}
	if err := s.republish(); err != nil {
		// The package is on disk but not in the agent's list. Roll the
		// directory back out so the store does not hold a package the
		// agent cannot see, which would be a package whose state nobody
		// can explain from the outside.
		_ = os.RemoveAll(s.dirFor(spec.Name, spec.Version))
		return s.fail(spec, err)
	}
	return ports.PluginState{
		Name: spec.Name, Version: spec.Version, Digest: digest, Installed: true,
		Replaced: replaced,
	}
}

// replacedBy reports the installed version of a package, if any.
//
// It reads every installed directory rather than looking for a
// "<name>-<oldVersion>" path, because the old version's directory is named
// by the version that was installed and the store has no other record of
// it.
func (s *pluginStore) replacedBy(spec ports.PluginSpec) *ports.PluginInfo {
	dirs, err := s.installedDirs()
	if err != nil {
		return nil
	}
	for _, p := range dirs {
		if p.broken != "" || p.Name != spec.Name {
			continue
		}
		if p.Version == spec.Version {
			// The same version is already here. This is a re-install of
			// what the node is already running, not an upgrade, and
			// reporting it as having replaced itself would make a
			// rollback restore the version it just removed.
			return nil
		}
		return &ports.PluginInfo{Name: p.Name, Version: p.Version, Digest: p.Digest}
	}
	return nil
}

// activate moves a reviewed package directory into the package store.
//
// It is the package directory that moves, not the staging directory that
// holds it. Renaming the wrapper would put the store's entry one level
// deeper than every other entry, so the store would list a directory
// whose manifest is not where a package's manifest is — and a package
// whose manifest cannot be found does not load.
//
// The old version's directory is kept rather than deleted, and this is the
// reason the method is separate from Remove: a rollback needs the previous
// package back, and a store that overwrites in place has thrown it away.
func (s *pluginStore) activate(spec ports.PluginSpec, root string) error {
	final := s.dirFor(spec.Name, spec.Version)
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return fmt.Errorf("create the package directory: %w", err)
	}
	if _, err := os.Stat(final); err == nil {
		// Same name and version already on disk. The digest check above
		// already established that what is there matches what was
		// offered, so replacing it is a no-op — and saying so is better
		// than an error, because a rollout retried against a node that
		// succeeded and lost its reply should converge, not fail.
		return nil
	}
	// Rename within one filesystem, so the package either appears whole
	// or not at all. A copy would have a window in which half the files
	// are present and the agent could read them. The staging directory
	// lives inside the package directory, so this is a same-filesystem
	// rename on every filesystem this has ever run on.
	//
	// The caller's deferred cleanup of the staging path stays armed and
	// is correct: a rename moves the directory rather than copying it, so
	// the old path no longer exists and removing what remains of the
	// staging tree does not touch what was just installed.
	if err := os.Rename(root, final); err != nil {
		return fmt.Errorf("activate %s: %w", spec.Name, err)
	}
	return nil
}

// Remove takes a package off.
//
// An empty version removes whatever is there. A version that is set is a
// guard: the node compares it against what it actually has and refuses on
// a mismatch. That refusal is the whole point of passing it. A rollback
// that raced a newer release would otherwise remove the *new* package
// while reporting success, and the operator would watch a fleet go back
// to nothing while believing it went back to the previous version.
func (s *pluginStore) Remove(_ context.Context, name, version string) ports.PluginState {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(name) == "" {
		return ports.PluginState{Name: name, Error: "no package named"}
	}
	dirs, err := s.installedDirs()
	if err != nil {
		return ports.PluginState{Name: name, Error: err.Error()}
	}
	found := false
	for _, p := range dirs {
		// A broken directory is left alone: its manifest says nothing
		// about what it is, and the directory name is a guess. Removing
		// "opskeeper-sre-readonly-0.1.0" on request for a package called
		// something else would be a removal nobody asked for.
		if p.broken != "" || p.Name != name {
			continue
		}
		if version != "" && p.Version != version {
			// Refused, not "removed anyway". The caller asked for a
			// specific version to come off and a different one is what is
			// installed, which means this node has moved on since the
			// request was composed. Reporting success here would be a
			// rollback that lied.
			return ports.PluginState{
				Name: name, Version: p.Version,
				Refused: fmt.Sprintf("asked to remove %s but %s is installed", version, p.Version),
			}
		}
		found = true
		if err := os.RemoveAll(p.Dir); err != nil {
			return ports.PluginState{Name: name, Error: fmt.Sprintf("remove %s: %v", filepath.Base(p.Dir), err)}
		}
	}
	if !found {
		// Not an error. A rollback that has already happened should
		// succeed, and a manager that removes a package it believes it
		// installed should not be told off for being right about nothing.
		return s.state(name, "", "was not installed")
	}
	if err := s.republish(); err != nil {
		return ports.PluginState{Name: name, Error: err.Error()}
	}
	return s.state(name, "", "removed")
}

// Installed reports the active set, sorted.
func (s *pluginStore) Installed() []ports.PluginInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeInfos()
}

// validateSpec refuses a request that could not be acted on safely.
func (s *pluginStore) validateSpec(spec ports.PluginSpec) error {
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("no package name was given")
	}
	if strings.TrimSpace(spec.Version) == "" {
		return errors.New("no version was given")
	}
	// The name goes into a directory path, so it may not be one. A
	// "../" here would be a write outside the package store, chosen by
	// whoever composed the request.
	if strings.ContainsAny(spec.Name, `/\`) || spec.Name == "." || spec.Name == ".." {
		return fmt.Errorf("package name %q is not a plain name", spec.Name)
	}
	if strings.ContainsAny(spec.Version, `/\`) || spec.Version == "." || spec.Version == ".." {
		return fmt.Errorf("version %q is not a plain version", spec.Version)
	}
	if strings.TrimSpace(spec.Signature) == "" {
		return errors.New("no signature was given; this node cannot verify a package without one")
	}
	lower := strings.ToLower(strings.TrimSpace(spec.SHA256))
	if len(lower) != 64 {
		return fmt.Errorf("sha256 must be 64 hex characters, got %d", len(lower))
	}
	if _, err := hex.DecodeString(lower); err != nil {
		return fmt.Errorf("sha256 is not hex: %w", err)
	}
	// http and https only. A file:// URL would make the package a local
	// file the caller chose rather than one this node fetched and checked.
	switch {
	case strings.HasPrefix(spec.URL, "https://"):
	case strings.HasPrefix(spec.URL, "http://"):
	default:
		return fmt.Errorf("url must be http or https, got %q", firstN(spec.URL, 20))
	}
	return nil
}

// download fetches the body and checks the transport digest.
func (s *pluginStore) download(ctx context.Context, spec ports.PluginSpec) ([]byte, error) {
	fetch := s.fetch
	if fetch == nil {
		fetch = httpPluginFetch
	}
	body, err := fetch(ctx, spec.URL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", spec.Name, err)
	}
	// Bounded after the read as well as during it. A fetcher that
	// returns a huge body is a fetcher that should not have been able to
	// finish, and the check after is what stops the digest from being
	// computed over gigabytes of somebody else's choice.
	if len(body) > maxPluginBundleBytes {
		return nil, fmt.Errorf("package body is %d bytes, over the %d byte cap",
			len(body), maxPluginBundleBytes)
	}
	return body, nil
}

// verifyDigest checks the transport digest.
//
// It is separate from download because a mismatch is a *refusal* rather
// than a failure: the body is not the one that was offered, and no
// amount of retrying will produce it. A manager that reads a mismatch as a
// transport fault retries it against every node in the fleet, and every
// node says the same thing.
func verifyDigest(body []byte, want string) error {
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != strings.ToLower(want) {
		return fmt.Errorf("package body digest is %s, not the %s that was offered", got, want)
	}
	return nil
}

// httpPluginFetch is the default transport.
func httpPluginFetch(ctx context.Context, url string) ([]byte, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, pluginDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server answered %s", resp.Status)
	}
	// Read one byte past the cap so an oversized body is detectable
	// rather than silently truncated into something that might still
	// hash correctly by accident.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPluginBundleBytes+1))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// dirFor is where a package lives.
func (s *pluginStore) dirFor(name, version string) string {
	return filepath.Join(s.pkgDir, name+"-"+version)
}

// installedPackage is one directory on disk and what it says it is.
//
// The two are carried separately and never derived from one another. The
// directory is named "<name>-<version>" for humans and for uniqueness, but
// a package name may itself contain a dash and a version may contain
// characters that are awkward in a path, so the name is read from the
// manifest and not parsed out of the directory name. A Remove that
// derived the name by splitting the string would silently fail to find
// every package whose name has a dash in it — which is the shape of every
// package in this repository.
type installedPackage struct {
	Dir     string
	Name    string
	Version string
	// Digest is recomputed from the tree, never remembered from the
	// request that installed it.
	Digest string
	// broken is why this directory no longer loads, when it does not.
	// A non-empty broken is what makes a republish fail rather than
	// quietly shrink the agent's package list.
	broken string
}

// installedDirs lists the package directories, skipping staging debris.
//
// A dot-prefixed entry is a staging directory from an install that did not
// finish, or a lock. Either way it is not a package, and treating it as
// one would put a directory in the agent's package list that no review
// ever passed.
func (s *pluginStore) installedDirs() ([]installedPackage, error) {
	entries, err := os.ReadDir(s.pkgDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []installedPackage
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(s.pkgDir, e.Name())
		p, err := pluginmanifest.Load(dir)
		if err != nil {
			// Recorded, not skipped. A directory that stopped loading is
			// a package that stopped working, and quietly omitting it here
			// would make republish drop it from the agent's list without
			// saying so — a capability disappearing, discovered later as
			// a model reporting it does not have a tool it had an hour
			// ago. The name is the directory's, because the manifest is
			// exactly what is unreadable.
			out = append(out, installedPackage{Dir: dir, Name: filepath.Base(dir), broken: err.Error()})
			continue
		}
		digest, _ := pluginmanifest.TreeDigest(dir)
		out = append(out, installedPackage{
			Dir: dir, Name: p.Name(), Version: p.Manifest.Metadata.Version, Digest: digest,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// activeInfos renders what the agent has been told it has.
//
// It reads the settings file rather than the package directory, and that
// is the whole reason it does. The store and the agent can legitimately
// disagree for a moment: a package can be on disk and no longer pass
// review, in which case republish has dropped it from the list and
// returned an error. Reporting the directory here would say the node can
// call a tool the agent was never given, and a manager trusting this
// answer would count a capability the node does not have.
//
// A package directory is left on disk when that happens, deliberately.
// Dropping it from the list is the node refusing to run it; deleting it
// would be the node destroying something an operator may have put there,
// and a review can fail for a reason that is not the package's fault —
// an unreadable trust store, a policy that changed under it.
func (s *pluginStore) activeInfos() []ports.PluginInfo {
	settings := filepath.Join(s.workDir, agentConfigDirName(), agentSettingsFile)
	body, err := os.ReadFile(settings)
	if err != nil {
		// No list has been published yet, so the node has told the agent
		// nothing. Empty is the truth.
		return nil
	}
	var parsed agentSettings
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	out := make([]ports.PluginInfo, 0, len(parsed.Packages))
	for _, entry := range parsed.Packages {
		abs, err := urlFromFileURL(entry)
		if err != nil {
			continue
		}
		p, err := pluginmanifest.Load(abs)
		if err != nil {
			continue
		}
		digest, _ := pluginmanifest.TreeDigest(abs)
		out = append(out, ports.PluginInfo{
			Name: p.Name(), Version: p.Manifest.Metadata.Version, Digest: digest,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// urlFromFileURL is the inverse of fileURL.
func urlFromFileURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("package entry %q is not a file URL", raw)
	}
	return filepath.FromSlash(u.Path), nil
}

// republish rewrites the agent's package list from what is on disk.
//
// Every installed package is re-reviewed on the way through, not just
// trusted because it was reviewed when it arrived. The review is a function
// of the tree, and a tree can change underneath a node: an operator editing
// a file in the package directory, a restore from backup, a disk that came
// back with different contents. A node that republishes from the directory
// it finds, rather than from a list it remembers, is the only one that
// notices.
//
// A package that has stopped passing is *dropped from the list and then
// reported*, not reported without being dropped. Both halves are required
// and the order is the point:
//
//   - Refusing to write leaves the agent pointing at a package that no
//     longer loads. The agent would start, fail to load one of its
//     packages, and the model would have lost a tool with nothing in the
//     transcript to say why. A capability disappearing silently is worse
//     than a capability disappearing loudly.
//   - Dropping without reporting leaves an operator with a node that is
//     quietly smaller than they think, and no way to find out which
//     package went.
//
// So the list is written first, and the error is returned after.
func (s *pluginStore) republish() error {
	pol, err := s.reviewPolicy()
	if err != nil {
		return err
	}
	dirs, err := s.installedDirs()
	if err != nil {
		return err
	}
	// The boot bundle comes first and is reviewed on the same terms. It
	// is not dropped when it fails — see the field comment.
	roots := make([]string, 0, len(dirs)+len(s.base))
	for _, base := range s.base {
		decision := pluginmanifest.Review(base, s.trust, pol)
		if !decision.Allowed {
			return fmt.Errorf("%s is a boot package and no longer passes this node's review: %s",
				filepath.Base(base), decision)
		}
		roots = append(roots, base)
	}
	var dropped []string
	for _, p := range dirs {
		switch {
		case p.broken != "":
			dropped = append(dropped, fmt.Sprintf("%s (%s)", p.Name, p.broken))
			continue
		}
		decision := pluginmanifest.Review(p.Dir, s.trust, pol)
		if !decision.Allowed {
			dropped = append(dropped, decision.String())
			continue
		}
		roots = append(roots, p.Dir)
	}
	if _, err := writeAgentSettings(s.workDir, roots); err != nil {
		return fmt.Errorf("publish the package list: %w", err)
	}
	if len(dropped) > 0 {
		return fmt.Errorf("%d installed package(s) no longer pass this node's review and were dropped "+
			"from the agent's list: %s", len(dropped), strings.Join(dropped, "; "))
	}
	return nil
}

// refuse builds a refusal state.
func (s *pluginStore) refuse(spec ports.PluginSpec, reason string) ports.PluginState {
	st := ports.PluginState{Name: spec.Name, Version: spec.Version, Refused: reason}
	if s.log != nil {
		s.log.Warn("plugin package refused", "plugin", spec.Name, "version", spec.Version, "reason", reason)
	}
	return st
}

// fail builds a failure state.
func (s *pluginStore) fail(spec ports.PluginSpec, err error) ports.PluginState {
	if s.log != nil {
		s.log.Error("plugin package could not be installed",
			"plugin", spec.Name, "version", spec.Version, "err", err)
	}
	return ports.PluginState{Name: spec.Name, Version: spec.Version, Error: err.Error()}
}

// state builds the post-state for Remove.
//
// Installed is false either way, because after a successful Remove the
// package is gone, and reporting it as installed would be the one answer
// that makes a rollback look like it failed. What happened goes in Note,
// where it cannot be mistaken for a verdict.
func (s *pluginStore) state(name, version, note string) ports.PluginState {
	if s.log != nil {
		s.log.Info("plugin package removed", "plugin", name, "outcome", note)
	}
	return ports.PluginState{Name: name, Version: version, Installed: false, Note: note}
}

// packageRootWithin finds the single package directory inside a staging
// directory.
//
// A tarball that holds two top-level directories, or none, is not a
// package — it is a container of something, and guessing which entry was
// meant to be the package is how a package gets reviewed in one directory
// and installed from another.
func packageRootWithin(stage string) (string, error) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", fmt.Errorf("read the staged package: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(stage, e.Name()))
		}
	}
	if len(dirs) != 1 {
		names := make([]string, 0, len(dirs))
		for _, d := range dirs {
			names = append(names, filepath.Base(d))
		}
		sort.Strings(names)
		return "", fmt.Errorf("the archive holds %d top-level directories (%s); a package is exactly one",
			len(dirs), strings.Join(names, ", "))
	}
	return dirs[0], nil
}

// extractPluginTarGz unpacks a package archive into stage.
//
// The path checks are not defence against a hostile publisher, because a
// hostile publisher is already caught by the signature two steps later.
// They are against a bugged or truncated archive, and against a tarball
// that was assembled from a directory rather than built deliberately —
// and a node that writes outside its staging directory on the way to
// discovering that is a node that has already lost the property the rest
// of this file is about.
func extractPluginTarGz(body []byte, stage string) error {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var written int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			// Symlinks and devices in a plugin package are never needed
			// and are the classic way to make a later write land
			// somewhere else.
			return fmt.Errorf("archive entry %q has type %v; a plugin package holds files and directories only",
				hdr.Name, hdr.Typeflag)
		}
		clean, err := safeJoin(stage, hdr.Name)
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(clean, 0o750); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o750); err != nil {
			return err
		}
		f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return fmt.Errorf("create %s: %w", hdr.Name, err)
		}
		n, err := io.Copy(f, io.LimitReader(tr, maxPluginBundleBytes-written))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", hdr.Name, err)
		}
		written += n
		if written > maxPluginBundleBytes {
			return fmt.Errorf("package extracts to more than the %d byte cap", int64(maxPluginBundleBytes))
		}
	}
}

// safeJoin refuses a path that leaves its root.
//
// A ".." segment is refused rather than neutralised. Clamping it to the
// staging directory would keep the node safe and lose the evidence: the
// operator would be told the package installed, and the archive that tried
// to write outside it would be gone. A refusal names what the archive
// tried to do, and that name is the only thing that points at a publisher
// with a problem.
func safeJoin(root, name string) (string, error) {
	if name == "" {
		return "", errors.New("archive entry has no name")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("archive entry %q is an absolute path", name)
	}
	for _, seg := range strings.Split(filepath.ToSlash(name), "/") {
		if seg == ".." {
			return "", fmt.Errorf("archive entry %q escapes the staging directory", name)
		}
	}
	joined := filepath.Join(root, filepath.Clean("/"+name))
	if joined != root && !strings.HasPrefix(joined, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %q escapes the staging directory", name)
	}
	return joined, nil
}

// firstN truncates a string for an error message, so a hostile URL does
// not become a hostile log line.
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

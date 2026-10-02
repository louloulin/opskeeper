// Module pig is the single boundary between OpsKeeper and PiG.
//
// Invariants:
//
//   - This is the ONLY OpsKeeper module permitted to import
//     github.com/MichaelKinsy/PiG. Every other module — manager, edge,
//     harness, and every third-party plugin — talks to the agent runtime
//     through core/ports instead. PiG is pre-stable 0.x and its API moves;
//     when it moves, this module absorbs the change and core, manager,
//     edge, harness, and every deployed plugin do not recompile.
//
//   - This module imports core/ports, never the reverse. The dependency
//     arrow points inward toward the contracts.
//
// Subpackages are split by which PiG package they adapt:
//
//	pigmodel  → github.com/MichaelKinsy/PiG/ai      (providers, models, streaming)
//	pigagent  → github.com/MichaelKinsy/PiG/agent   (the agent loop)
module github.com/vincent-wuhan/opskeeper/core/pig

go 1.26.0

require (
	github.com/MichaelKinsy/PiG v0.3.0
	github.com/vincent-wuhan/opskeeper/core v0.0.0
	// Test-only (pigprofile). See the replace note below.
	github.com/vincent-wuhan/opskeeper/core/edge v0.0.0
)

require (
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/MichaelKinsy/PiG/extensions/sdk v0.3.0 // indirect
	github.com/aws/aws-sdk-go-v2 v1.41.7 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.10 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.17 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.16 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/bedrockruntime v1.50.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.23 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.42.1 // indirect
	github.com/aws/smithy-go v1.25.1 // indirect
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace (
	// Development-time pin. PiG is pre-stable, so the workspace builds
	// against the local checkout; releases replace this with a fixed
	// tagged version.
	github.com/MichaelKinsy/PiG => /Users/louloulin/appx/PiG
	// Sibling modules resolve by path during development. The workspace
	// covers this in a normal build; the replace keeps `go build` working
	// from a bare module directory and in CI jobs that disable workspaces.
	github.com/vincent-wuhan/opskeeper/core => ../
	// The node profile contract test (pigprofile) is the only thing in
	// this module that reaches for core/edge, and it does so to read the
	// profile the node writes. It is the reverse of the direction the
	// architecture runs in, confined to a test, and it is here because
	// this is the one module that is allowed to know what PiG's profile
	// schema says — which is the whole reason the assertion lives here
	// rather than next to the code that renders the file.
	github.com/vincent-wuhan/opskeeper/core/edge => ../edge
)

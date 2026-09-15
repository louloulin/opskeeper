#!/usr/bin/env ruby
# frozen_string_literal: true
# build-qwenpaw-plugin.rb — Build opskeeper-teamharness QwenPaw zip from base package.
# Pattern mirrors AgentTeams plugins/teamharness/adapters/qwenpaw/scripts/build-qwenpaw-plugin.rb.

require "fileutils"
require "json"
require "open3"
require "pathname"
require "tmpdir"
require "yaml"

manifest_path = Pathname.new(ARGV[0] || "plugins/opskeeper-teamharness/plugin.yaml").expand_path
plugin_root = manifest_path.dirname
repo_root = plugin_root.parent.parent
adapter_root = plugin_root / "adapters/qwenpaw"
out_dir = Pathname.new(ENV["OUT_DIR"] || (plugin_root / "dist").to_s).expand_path

abort("missing manifest: #{manifest_path}") unless manifest_path.file?
abort("missing qwenpaw adapter: #{adapter_root}") unless adapter_root.directory?

manifest = YAML.load_file(manifest_path)
name = manifest.fetch("metadata").fetch("name")
version = manifest.fetch("metadata").fetch("version")
package_name = "#{name}-qwenpaw-#{version}"

def copy_entry(source_root, target_root, entry)
  src = source_root / entry
  abort("missing qwenpaw package source: #{src}") unless src.exist?
  dst = target_root / entry
  if src.directory?
    FileUtils.mkdir_p(dst)
    entries = Dir.glob((src / "*").to_s, File::FNM_DOTMATCH).reject do |path|
      [".", ".."].include?(File.basename(path))
    end
    FileUtils.cp_r(entries, dst)
  else
    FileUtils.mkdir_p(dst.dirname)
    FileUtils.cp(src, dst)
  end
end

def prune_generated(path)
  Dir.glob((path / "**/*").to_s, File::FNM_DOTMATCH).each do |item|
    base = File.basename(item)
    FileUtils.rm_rf(item) if base == "__pycache__" || base == ".DS_Store" || base.end_with?(".pyc")
  end
end

def zip_dir(root, package_name, out_path)
  FileUtils.rm_f(out_path)
  archive_helper = Pathname.new(__FILE__).realpath.dirname.join("../../../../../scripts/deterministic_archive.py")
  system(
    "python3",
    archive_helper.to_s,
    "zip",
    out_path.to_s,
    "--source",
    "#{root}/#{package_name}=#{package_name}"
  ) || abort("deterministic zip failed")
end

out_dir.mkpath
out_zip = out_dir / "#{package_name}.zip"
stable_zip = out_dir / "opskeeper-teamharness-qwenpaw.zip"

Dir.mktmpdir("opskeeper-teamharness-qwenpaw-") do |tmp|
  tmp_root = Pathname.new(tmp)
  staging = tmp_root / package_name
  asset_dir = staging / "opskeeper-teamharness"
  staging.mkpath
  asset_dir.mkpath

  %w[plugin.yaml prompts skills mcp].each do |entry|
    copy_entry(plugin_root, asset_dir, entry)
  end

  # qwenpaw-skills 暴露给 qwenpaw runtime 作为 public skills
  public_skills = asset_dir / "qwenpaw-skills"
  public_skills.mkpath
  %w[agent team].each do |group|
    Dir.glob((plugin_root / "skills" / group / "*").to_s).each do |skill|
      FileUtils.cp_r(skill, public_skills)
    end
  end

  copy_entry(adapter_root, staging, "plugin.py")
  copy_entry(adapter_root, staging, "task_trace.py")
  %w[LICENSE NOTICE.md].each { |entry| copy_entry(repo_root, staging, entry) }

  qwenpaw_manifest = {
    "id" => "opskeeper-teamharness",
    "name" => "Opskeeper TeamHarness",
    "version" => version,
    "type" => "general",
    "description" => "Opskeeper RCA/recovery plugin for AgentTeams QwenPaw workers.",
    "author" => "opskeeper-v2",
    "entry" => { "backend" => "plugin.py" },
    "dependencies" => [],
    "min_version" => "2.0.1",
    "qwenpaw_version" => { "min" => "2.0.1", "max" => "2.1.0" },
  }
  File.write(staging / "plugin.json", JSON.pretty_generate(qwenpaw_manifest))

  prune_generated(staging)
  zip_dir(tmp_root, package_name, out_zip)
  FileUtils.cp(out_zip, stable_zip)
end

puts out_zip

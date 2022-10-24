package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/Masterminds/semver"
	"github.com/cockroachdb/errors"
	_ "github.com/joho/godotenv/autoload"
	"github.com/nikoksr/simplog"
	"github.com/spf13/pflag"

	"github.com/nikoksr/mimikry/pkg/docker"
)

type (
	templateData struct {
		Version      string
		Maintainer   string
		InstallTools bool
		Tools        string
	}

	options struct {
		Versions      []string
		Maintainer    string
		TargetRepo    string
		TemplatePath  string
		BuildDir      string
		DryRun        bool
		Debug         bool
		KeepBuildDirs bool
	}

	buildContext struct {
		Version  *semver.Version
		IsLatest bool
		Client   *docker.Client
		Options  *options
	}

	versionsList []*semver.Version
)

func parseVersions(versions []string) versionsList {
	parsedVersions := make(versionsList, 0, len(versions))

	for _, version := range versions {
		parsedVersion, err := semver.NewVersion(version)
		if err != nil {
			log.Fatalf("Failed to parse version: %v", err)
		}

		parsedVersions = append(parsedVersions, parsedVersion)
	}

	return parsedVersions
}

func (r versionsList) Has(version *semver.Version) bool {
	if len(r) == 0 {
		return true
	}

	for _, v := range r {
		if v.Major() == version.Major() {
			return true
		}
	}

	return false
}

const (
	defaultSourceRepo     = "postgres" // TODO: Needs to be extracted from Dockerfile template
	defaultDockerTools    = "vim"
	defaultMaintainer     = "Unknown"
	defaultBuildDirectory = "./mimikry"
)

var templateDockerfile *template.Template

func setup(templatePath string) error {
	// Load template
	var err error
	templateDockerfile, err = template.ParseFiles(templatePath)
	if err != nil {
		return errors.Wrap(err, "load template")
	}

	return nil
}

func cleanPath(path string) string {
	return filepath.FromSlash(filepath.Clean(path))
}

func printHelp() {
	_, _ = fmt.Fprint(os.Stderr, `Usage:

  mimikry [OPTIONS] SOURCE-FILE TARGET-REPO

Options:

`)

	pflag.PrintDefaults()

	// Print example
	_, _ = fmt.Fprintf(os.Stderr, `
Example:

  # Build all versions for parent image of Dockerfile template and push them to docker repo "johndoe/some-image"
  mimikry -m "John Doe" Dockerfile.tmlp johndoe/postgres

  # Only build version 12.3 for parent image of Dockerfile template and push it to docker repo "johndoe/some-image"
  mimikry -m "John Doe" -v 12.3 Dockerfile.tmlp johndoe/postgres

`)
}

func optionsFromCLI() (*options, error) {
	var ops options

	pflag.StringVarP(&ops.Maintainer, "maintainer", "m", defaultMaintainer, "The maintainer of the Dockerfile")
	pflag.StringVarP(&ops.BuildDir, "build", "b", defaultBuildDirectory, "The path to the build directory")
	pflag.StringSliceVarP(&ops.Versions, "versions", "v", nil, "The versions (semver) to build")
	pflag.BoolVar(&ops.DryRun, "dry-run", false, "Enable dry run mode; build but don't push")
	pflag.BoolVar(&ops.Debug, "debug", false, "Enable debug mode")
	pflag.BoolVar(&ops.KeepBuildDirs, "keep", false, "Keep build directories after build")

	pflag.Usage = printHelp
	pflag.Parse()

	// Source file and target repo are required
	if pflag.NArg() != 2 {
		return nil, errors.New("missing arguments; see usage (-h) for more information")
	}

	// Set values from CLI args
	ops.TemplatePath = pflag.Arg(0)
	ops.TargetRepo = pflag.Arg(1)

	// Clean up some paths
	ops.TemplatePath = cleanPath(ops.TemplatePath)
	ops.BuildDir = cleanPath(ops.BuildDir)

	return &ops, nil
}

func getTagBuildDir(baseDir, version string) string {
	return filepath.FromSlash(filepath.Join(baseDir, version))
}

func createVersionDirectory(path string, version *semver.Version, opts *options) error {
	// Create directory for version if it doesn't exist
	if err := os.MkdirAll(path, 0o750); err != nil {
		return errors.Wrap(err, "create tag directory")
	}

	// Open Dockerfile for version
	dockerfilePath := filepath.Join(path, "Dockerfile")
	dockerfile, err := os.Create(dockerfilePath)
	if err != nil {
		return errors.Wrap(err, "create Dockerfile")
	}

	// TODO: Remove specific use-case
	installTools := !version.LessThan(semver.MustParse("10.0.0"))

	// Execute template
	data := templateData{
		Version:      version.Original(),
		Maintainer:   opts.Maintainer,
		InstallTools: installTools,
		Tools:        defaultDockerTools, // TODO: Make this configurable
	}

	if err = templateDockerfile.Execute(dockerfile, data); err != nil {
		return errors.Wrap(err, "execute template")
	}

	return nil
}

func cleanupBuildDirs(ctx context.Context, dirs []string) {
	logger := simplog.FromContext(ctx)

	for _, dir := range dirs {
		logger.Debugf("Removing build directory %s", dir)
		if err := os.RemoveAll(dir); err != nil {
			logger.Errorf("Failed to remove build directory %s: %v", dir, err)
		}
	}
}

func main() {
	// Create signal cancel context
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	// Get options from CLI
	opts, err := optionsFromCLI()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	// Setup logger
	logger := simplog.NewClientLogger(opts.Debug)
	ctx = simplog.WithLogger(ctx, logger)

	// Setup
	if err = setup(opts.TemplatePath); err != nil {
		logger.Error(errors.Wrap(err, "setup"))
		os.Exit(1)
	}

	// Run main
	if err = realMain(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error(err)
		os.Exit(1)
	}
}

func buildImage(ctx context.Context, buildCtx *buildContext) error {
	logger := simplog.FromContext(ctx)
	logger.Debugf("Building version %s", buildCtx.Version.Original())

	// Get full tag for image
	var tags []string
	image := docker.FullTag(buildCtx.Options.TargetRepo, buildCtx.Version.Original())
	tags = append(tags, image)

	// Build image
	dockerfilePath := filepath.Join(defaultBuildDirectory, buildCtx.Version.Original(), "Dockerfile")
	logger.Infof("Building image %s", image)

	imageID, err := buildCtx.Client.Image.Build(ctx, dockerfilePath, buildCtx.Options.TargetRepo)
	if err != nil {
		return errors.Wrap(err, "build image")
	}

	// In case of the last image, also tag it as latest; this expects the version list to be sorted the oldest first
	if buildCtx.IsLatest {
		logger.Infof("Tagging image %s as latest", image)
		latestTag := docker.FullTag(buildCtx.Options.TargetRepo, "latest")
		tags = append(tags, latestTag)
	}

	// Tag image
	logger.Infof("Tagging image %s", image)
	if err = buildCtx.Client.Image.Tag(ctx, imageID, tags...); err != nil {
		return errors.Wrap(err, "tag image")
	}

	// Push image
	if !buildCtx.Options.DryRun {
		logger.Infof("Pushing image %s", image)
		err = buildCtx.Client.Image.Push(ctx, tags...)
		if err != nil {
			return errors.Wrap(err, "push image")
		}
	}

	// Remove image to save space
	err = buildCtx.Client.Image.Remove(ctx, imageID)
	if err != nil {
		return errors.Wrap(err, "remove image")
	}

	// Draw checkmark with fixed distance to the left
	logger.Infof("Done with image %s", image)

	return nil
}

func realMain(ctx context.Context, opts *options) error {
	logger := simplog.FromContext(ctx)

	// Create docker client
	logger.Debug("Creating docker client")
	client, err := docker.New(ctx)
	if err != nil {
		return errors.Wrap(err, "create docker client")
	}
	defer func() { _ = client.Close(ctx) }()

	// Login
	if !opts.DryRun {
		logger.Info("Logging in to docker")
		if err = client.LoginFromEnv(ctx); err != nil {
			return errors.Wrap(err, "login to dockerhub")
		}
		defer func() { _ = client.Logout(ctx) }()
	}

	// Load remoteTags
	logger.Info("Loading remote tags")
	versions, err := docker.GetDockerHubRepoTags(ctx, defaultSourceRepo)
	if err != nil {
		return errors.Wrap(err, "load remote tags")
	}

	// Build directory tree and generate Dockerfile from template for each version
	logger.Info("Building and uploading images")

	// Parse versions
	requestedVersions := parseVersions(opts.Versions)
	logger.Debugf("Requested versions: %v", requestedVersions)

	// Create build context
	buildCtx := &buildContext{
		Client:  client,
		Options: opts,
	}

	// Setup guaranteed cleanup
	var pathsToCleanup []string
	defer func() { cleanupBuildDirs(ctx, pathsToCleanup) }()

	// Build and push all images
	for idx, version := range versions {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			logger.Debugf("Processing version %d/%d: %s", idx+1, len(versions), version.Original())

			// Check if version is requested
			if !requestedVersions.Has(version) {
				logger.Debugf("Skipping version %s", version.Original())
				continue
			}

			// Update the build context
			buildCtx.Version = version
			buildCtx.IsLatest = idx == len(versions)-1

			// Create build directory
			buildDirectory := getTagBuildDir(opts.BuildDir, version.Original())
			if err = createVersionDirectory(buildDirectory, buildCtx.Version, buildCtx.Options); err != nil {
				return errors.Wrap(err, "create build directory")
			}

			// If the user does not want to keep the build directories, add them to the cleanup list
			if !opts.KeepBuildDirs {
				pathsToCleanup = append(pathsToCleanup, buildDirectory)
			}

			// Build image
			if err = buildImage(ctx, buildCtx); err != nil {
				logger.Errorf("Failed to build image for version %s: %s", version.Original(), err)
			}
		}
	}

	return nil
}

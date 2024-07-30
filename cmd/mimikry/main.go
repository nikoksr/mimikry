package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/semver/v3"
	_ "github.com/joho/godotenv/autoload"
	"github.com/nikoksr/simplog"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

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
		VersionConstraint string
		TagLatest         bool
		Maintainer        string
		TargetRepo        string
		TemplatePath      string
		BuildDir          string
		DryRun            bool
		Debug             bool
		KeepBuildDirs     bool
	}

	imageTags struct {
		Image    string    `json:"image"`
		Modified time.Time `json:"modified"`
		Tags     []string  `json:"tags"`
	}
)

const (
	defaultSourceRepo     = "postgres" // TODO: Needs to be extracted from Dockerfile template
	defaultDockerTools    = "vim"
	defaultMaintainer     = "Unknown"
	defaultBuildDirectory = "./mimikry"
	postgresCachePath     = "./.cache/mimikry/postgres.json"
)

var (
	ErrNoTagCache      = errors.New("no tag cache found")
	ErrInvalidTagCache = errors.New("invalid tag cache")

	patternImageTag = regexp.MustCompile(`^\d+(\.\d+)?(\.\d+)?$`) // Ignore anything that is not a major.minor version

	stdSkipTagFunc = func(tag string) bool {
		return !patternImageTag.MatchString(tag)
	}
)

func loadTagCache(path string) (*imageTags, error) {
	var cache imageTags

	// Open file
	file, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("open tag cache file: %w", err)
		}

		return nil, ErrNoTagCache
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat tag cache file: %w", err)
	}

	if info.Size() == 0 {
		return nil, ErrNoTagCache
	}

	// Decode JSON
	if err = json.NewDecoder(file).Decode(&cache); err != nil {
		return nil, fmt.Errorf("decode tag cache: %w", err)
	}

	if cache.Image == "" || len(cache.Tags) == 0 {
		return nil, ErrInvalidTagCache
	}

	if !info.ModTime().IsZero() && info.ModTime().After(cache.Modified) { // Probably redundant, but just to be sure
		cache.Modified = info.ModTime()
	}

	return &cache, nil
}

func saveTagCache(path string, cache *imageTags) error {
	// Create directory
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create tag cache directory: %w", err)
	}

	// Open file
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create tag cache file: %w", err)
	}
	defer func() { _ = file.Close() }()

	// Encode JSON
	if err = json.NewEncoder(file).Encode(cache); err != nil {
		return fmt.Errorf("encode tag cache: %w", err)
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

  # Build all versions for parent image of Dockerfile template and push them to the given docker repo
  mimikry my-templates johndoe/some-repo

  # Only build version 12.3 for parent image of Dockerfile template and push it to the given docker repo
  mimikry -v "12.3" my-templates/ johndoe/some-repo

  # Build versions that are greater than or equal to 12.3 for parent image of Dockerfile template and push them to the given docker repo
  mimikry -v ">= 12.3" my-templates/ johndoe/some-repo

  # Build versions that are greater than or equal to 12.0 and less than 13.0 for parent image of Dockerfile template and push them to the given docker repo and tag the latest image
  mimikry -v "^12" --latest my-templates/ johndoe/some-repo

  # For more info about version constraints, read here: https://github.com/Masterminds/semver?tab=readme-ov-file#basic-comparisons
`)
}

func optionsFromCLI() (*options, error) {
	var ops options

	pflag.StringVarP(&ops.Maintainer, "maintainer", "m", defaultMaintainer, "The maintainer of the Dockerfile")
	pflag.StringVarP(&ops.BuildDir, "build", "b", defaultBuildDirectory, "The path to the build directory")
	pflag.StringVarP(&ops.VersionConstraint, "version", "v", "", "Semantic version constraint; e.g. \">= 12.3\". If not set, all versions are built. See -h for more information")
	pflag.BoolVarP(&ops.TagLatest, "latest", "l", false, "Whether to tag the latest image as latest")
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

func prepareBuildDirectory(path string, version *semver.Version, templates *template.Template, opts *options) error {
	// Create directory for version if it doesn't exist
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create build directory: %w", err)
	}

	eg := &errgroup.Group{}

	for _, rawTemplate := range templates.Templates() {
		rawTemplate := rawTemplate
		eg.Go(func() error {
			// Open Dockerfile for version
			outputPath := filepath.Join(path, rawTemplate.Name())
			outputFile, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("create template %q: %w", rawTemplate.Name(), err)
			}
			defer outputFile.Close()

			// TODO: Remove specific use-case
			installTools := !version.LessThan(semver.MustParse("10.0.0"))

			// Execute template
			data := templateData{
				Version:      version.Original(),
				Maintainer:   opts.Maintainer,
				InstallTools: installTools,
				Tools:        defaultDockerTools, // TODO: Make this configurable
			}

			if err = rawTemplate.Execute(outputFile, data); err != nil {
				return fmt.Errorf("execute template %q: %w", rawTemplate.Name(), err)
			}

			return nil
		})
	}

	return eg.Wait()
}

func cleanupBuildDirs(ctx context.Context, dirs []string) {
	logger := simplog.FromContext(ctx)

	for _, dir := range dirs {
		dir = filepath.FromSlash(dir)

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

	// Parse all template files in the template directory
	templates, err := template.ParseGlob(filepath.Join(opts.TemplatePath, "*"))
	if err != nil {
		logger.Error(err)
		os.Exit(1)
	}

	// Run main
	if err = realMain(ctx, templates, opts); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error(err)
		os.Exit(1)
	}
}

func realMain(ctx context.Context, templates *template.Template, opts *options) error {
	logger := simplog.FromContext(ctx)

	// Parse the versions constraint
	versionConstraint, err := semver.NewConstraint(opts.VersionConstraint)
	if err != nil {
		return fmt.Errorf("parse version constraint: %w", err)
	}
	logger.Debugf("Parsed version constraint: %s", versionConstraint)

	// Create docker client
	logger.Debug("Creating docker client")
	client, err := docker.New(ctx)
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer func() { _ = client.Close(ctx) }()

	// Login
	if !opts.DryRun {
		logger.Info("Logging in to docker")
		if err = client.LoginFromEnv(ctx); err != nil {
			return fmt.Errorf("login to docker: %w", err)
		}
		defer func() { _ = client.Logout(ctx) }()
	} else {
		logger.Info("Dry run enabled; skipping authentication")
	}

	// Try to load tags from cache
	logger.Info("Loading image tags")
	logger.Debug("Trying to load tag cache")

	tags, err := loadTagCache(postgresCachePath)
	if err != nil {
		logger.Debugf("Failed to load tag cache: %v", err)
	}

	if tags != nil {
		logger.Debug("Using tag cache")
	} else {
		logger.Debug("No tag cache found; loading remote tags")
		tagList, err := docker.GetDockerHubRepoTags(ctx, defaultSourceRepo)
		if err != nil {
			return fmt.Errorf("load remote tags: %w", err)
		}

		// Create tag cache
		tags = &imageTags{
			Image:    defaultSourceRepo,
			Modified: time.Now(),
			Tags:     tagList,
		}
	}

	numTags := len(tags.Tags)
	logger.Debugf("Loaded %d tags", numTags)

	// Pre-sort and -filter tags; this does worsen the performance technically, but it avoids a lot
	// of issues down the line.
	versions := make([]*semver.Version, 0, numTags)
	for _, tag := range tags.Tags {
		// Sanitize tag and skip if it's not a major.minor version
		tag = strings.TrimSpace(tag)
		if stdSkipTagFunc(tag) {
			// Not removing the tag from the list as it might be requested by the user later
			logger.Debugf("Skipping version %s; not a major.minor version", tag)
			continue
		}

		version, err := semver.NewVersion(tag)
		if err != nil {
			logger.Warnf("Failed to parse tag %s: %v", tag, err)
			continue
		}

		// Check if the version matches the constraint
		if !versionConstraint.Check(version) {
			logger.Debugf("Skipping version %s; does not match constraint", tag)
			continue
		}

		// Finally, add the version to the list
		logger.Debugf("Adding version %s", tag)
		versions = append(versions, version)
	}

	sort.Sort(semver.Collection(versions))
	numTags = len(versions)
	logger.Debugf("%d tags after sorting and filtering", numTags)

	// Build directory tree and generate Dockerfile from template for each version
	logger.Info("Building and uploading images")

	// Persist tags to cache file and cleanup build directories
	var pathsToCleanup []string
	defer func() {
		// Save tag cache; it's deferred as the main loop might alter the tags
		logger.Debug("Saving tag cache")
		if err = saveTagCache(postgresCachePath, tags); err != nil {
			logger.Errorf("Failed to save tag cache: %v", err)
		}

		// Cleanup build directories
		cleanupBuildDirs(ctx, pathsToCleanup)
	}()

	// Build and push all images
	previousImage := ""
	previousBaseImage := ""
	for idx, version := range versions {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		logger.Debugf("Processing tag %d/%d: %s", idx+1, numTags, version)

		// Create build directory
		buildDirectory := getTagBuildDir(opts.BuildDir, version.Original())
		if err = prepareBuildDirectory(buildDirectory, version, templates, opts); err != nil {
			return fmt.Errorf("create version directory: %w", err)
		}

		// If the user does not want to keep the build directories, add them to the cleanup list
		if !opts.KeepBuildDirs {
			pathsToCleanup = append(pathsToCleanup, buildDirectory)
		}

		// If this is the last image, tag it as latest
		imageTag := fmt.Sprintf("%s:%s", opts.TargetRepo, version.Original())
		tags := []string{imageTag}
		if opts.TagLatest && idx == numTags-1 {
			tags = append(tags, fmt.Sprintf("%s:%s", opts.TargetRepo, "latest"))
			logger.Infof("Tagging image %s as latest", imageTag)
		}

		// Build image
		buildDirectory = filepath.Join(defaultBuildDirectory, version.Original())

		logger.Infof("Building image %s", imageTag)
		imageID, baseID, err := client.Images().Build(ctx, buildDirectory, tags...)
		if err != nil {
			return fmt.Errorf("build image: %w", err)
		}

		if imageID == "" || baseID == "" {
			return fmt.Errorf("build image: %w", errors.New("image id or base id is empty"))
		}

		logger.Debugf("Image %s built based on parent image %s", imageID, baseID)

		// Push image
		if !opts.DryRun {
			logger.Infof("Pushing image %s", imageTag)
			err = client.Images().Push(ctx, tags...)
			if err != nil {
				return fmt.Errorf("push image: %w", err)
			}
		} else {
			logger.Infof("Dry run enabled; skipping push for image %s", imageTag)
		}

		// Clean-up

		// Remove images
		imagesToRemove := make([]string, 0, 2)

		if previousImage != "" {
			imagesToRemove = append(imagesToRemove, previousImage)
		}

		if previousBaseImage != "" {
			imagesToRemove = append(imagesToRemove, previousBaseImage)
		}

		if len(imagesToRemove) > 0 {
			logger.Infof("Removing build artifacts")
			if err = client.Images().Remove(ctx, imagesToRemove...); err != nil {
				return fmt.Errorf("remove images: %w", err)
			}
		}

		previousImage = imageID
		previousBaseImage = baseID

		logger.Infof("Done with image %s", version.Original())
	}

	return nil
}

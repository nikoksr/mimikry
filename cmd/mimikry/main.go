// Package main provides the mimikry CLI tool for building locale-modified Docker images.
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
	"github.com/nikoksr/mimikry/pkg/docker"
	"github.com/nikoksr/simplog"
	"github.com/spf13/pflag"
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
		SourceRepo        string
		Tools             string
		TargetRepo        string
		TemplatePath      string
		BuildDir          string
		DryRun            bool
		Debug             bool
		KeepBuildDirs     bool
		Force             bool
	}

	imageTags struct {
		Image    string    `json:"image"`
		Modified time.Time `json:"modified"`
		Tags     []string  `json:"tags"`
	}
)

const (
	defaultMaintainer     = "Unknown"
	defaultBuildDirectory = "./build"
)

var (
	errNoTagCache      = errors.New("no tag cache found")
	errInvalidTagCache = errors.New("invalid tag cache")

	patternImageTag = regexp.MustCompile(`^\d+(\.\d+)?$`) // Match major-only (e.g., 18) and major.minor (e.g., 18.3) versions
)

func shouldSkipTag(tag string) bool {
	return !patternImageTag.MatchString(tag)
}

func loadTagCache(path string) (*imageTags, error) {
	var cache imageTags

	// Open file
	file, err := os.Open(path) // nosec:G304 -- path is constructed from known source repo name
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("open tag cache file: %w", err)
		}

		return nil, errNoTagCache
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat tag cache file: %w", err)
	}

	if info.Size() == 0 {
		return nil, errNoTagCache
	}

	// Decode JSON
	if err = json.NewDecoder(file).Decode(&cache); err != nil {
		return nil, fmt.Errorf("decode tag cache: %w", err)
	}

	if cache.Image == "" || len(cache.Tags) == 0 {
		return nil, errInvalidTagCache
	}

	return &cache, nil
}

func saveTagCache(path string, cache *imageTags) error {
	// Create directory
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create tag cache directory: %w", err)
	}

	// Open file
	file, err := os.Create(path) // nosec:G304 -- path is constructed from known source repo name
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

  mimikry [OPTIONS] TEMPLATE-PATH TARGET-REPO

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

  # Force rebuild all versions, even if they already exist in the target repo
  mimikry --force my-templates/ johndoe/some-repo

  # For more info about version constraints, read here: https://github.com/Masterminds/semver?tab=readme-ov-file#basic-comparisons
`)
}

func optionsFromCLI() (*options, error) {
	var opts options

	pflag.StringVarP(&opts.Maintainer, "maintainer", "m", defaultMaintainer, "The maintainer of the Dockerfile")
	pflag.StringVarP(&opts.BuildDir, "build", "b", defaultBuildDirectory, "The path to the build directory")
	pflag.StringVarP(&opts.VersionConstraint, "version", "v", "", "Semantic version constraint; e.g. \">= 12.3\". If not set, all versions are built. See -h for more information")
	pflag.StringVar(&opts.SourceRepo, "source", "postgres", "The source Docker Hub repository to fetch tags from")
	pflag.StringVar(&opts.Tools, "tools", "vim", "Comma-separated list of tools to install in the image; empty string means no tools")
	pflag.BoolVarP(&opts.TagLatest, "latest", "l", false, "Whether to tag the latest image as latest")
	pflag.BoolVar(&opts.DryRun, "dry-run", false, "Enable dry run mode; build but don't push")
	pflag.BoolVar(&opts.Debug, "debug", false, "Enable debug mode")
	pflag.BoolVar(&opts.KeepBuildDirs, "keep", false, "Keep build directories after build")
	pflag.BoolVar(&opts.Force, "force", false, "Force rebuild of all versions; ignore existing tags in target repo")

	pflag.Usage = printHelp
	pflag.Parse()

	// Source file and target repo are required
	if pflag.NArg() != 2 {
		return nil, errors.New("missing arguments; see usage (-h) for more information")
	}

	// Set values from CLI args
	opts.TemplatePath = pflag.Arg(0)
	opts.TargetRepo = pflag.Arg(1)

	// Clean up some paths
	opts.TemplatePath = cleanPath(opts.TemplatePath)
	opts.BuildDir = cleanPath(opts.BuildDir)

	return &opts, nil
}

func getTagBuildDir(baseDir, version string) string {
	return filepath.FromSlash(filepath.Join(baseDir, version))
}

func prepareBuildDirectory(path string, version *semver.Version, templates *template.Template, opts *options) error {
	// Create directory for version if it doesn't exist
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create build directory: %w", err)
	}

	for _, rawTemplate := range templates.Templates() {
		// Open output file for this template
		outputPath := filepath.Join(path, rawTemplate.Name())
		outputFile, err := os.Create(outputPath) // nosec:G304 -- path is constructed from version + template name
		if err != nil {
			return fmt.Errorf("create template %q: %w", rawTemplate.Name(), err)
		}

		// Execute template
		data := templateData{
			Version:      version.Original(),
			Maintainer:   opts.Maintainer,
			InstallTools: opts.Tools != "",
			Tools:        opts.Tools,
		}

		if err = rawTemplate.Execute(outputFile, data); err != nil {
			outputFile.Close()
			return fmt.Errorf("execute template %q: %w", rawTemplate.Name(), err)
		}

		_ = outputFile.Close()
	}

	return nil
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
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

	// Build cache path from source repo name
	cachePath := filepath.Join(".cache", "mimikry", opts.SourceRepo+".json")

	// Parse the versions constraint; empty string means all versions match.
	var versionConstraint *semver.Constraints
	if opts.VersionConstraint != "" {
		var err error
		versionConstraint, err = semver.NewConstraint(opts.VersionConstraint)
		if err != nil {
			return fmt.Errorf("parse version constraint: %w", err)
		}
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

	// Load tags; prefer fresh from Docker Hub, fall back to cache
	logger.Info("Loading image tags")

	var tags *imageTags

	// Try remote first
	tagList, err := docker.GetDockerHubRepoTags(ctx, opts.SourceRepo)
	if err != nil {
		logger.Warnf("Failed to fetch remote tags: %v", err)
		logger.Info("Falling back to cached tags")

		cachedTags, cacheErr := loadTagCache(cachePath)
		if cacheErr != nil {
			return fmt.Errorf("fetch remote tags failed and no usable cache: %w", err)
		}
		tags = cachedTags
	} else {
		tags = &imageTags{
			Image:    opts.SourceRepo,
			Modified: time.Now(),
			Tags:     tagList,
		}
	}

	totalRemoteTags := len(tags.Tags)
	logger.Debugf("Loaded %d tags", totalRemoteTags)

	// Filter and sort tags upfront to reduce iterations in the build loop and ensure predictable ordering.
	versions := make([]*semver.Version, 0, totalRemoteTags)
	for _, tag := range tags.Tags {
		// Sanitize tag and skip if it's not a major.minor version
		tag = strings.TrimSpace(tag)
		if shouldSkipTag(tag) {
			// Not removing the tag from the list as it might be requested by the user later
			logger.Debugf("Skipping tag %s; does not match version pattern", tag)
			continue
		}

		version, err := semver.NewVersion(tag)
		if err != nil {
			logger.Warnf("Failed to parse tag %s: %v", tag, err)
			continue
		}

		// Check if the version matches the constraint
		if versionConstraint != nil && !versionConstraint.Check(version) {
			logger.Debugf("Skipping version %s; does not match constraint", tag)
			continue
		}

		// Finally, add the version to the list
		logger.Debugf("Adding version %s", tag)
		versions = append(versions, version)
	}

	sort.Sort(semver.Collection(versions))
	logger.Debugf("%d tags after sorting and filtering", len(versions))

	// Incremental build: skip versions already present in target repo
	if !opts.Force {
		targetTags, targetErr := docker.GetDockerHubRepoTags(ctx, opts.TargetRepo)
		if targetErr != nil {
			logger.Warnf("Failed to fetch target repo tags: %v", targetErr)
			logger.Info("Building all versions; target repo may be empty or unreachable")
		} else {
			existingTags := make(map[string]struct{}, len(targetTags))
			for _, t := range targetTags {
				existingTags[t] = struct{}{}
			}

			before := len(versions)
			filtered := versions[:0]
			for _, v := range versions {
				if _, exists := existingTags[v.Original()]; exists {
					logger.Debugf("Skipping version %s; already exists in target repo", v.Original())
					continue
				}
				filtered = append(filtered, v)
			}
			versions = filtered
			logger.Infof("Skipping %d versions already present in target repo (%d remain)", before-len(versions), len(versions))
		}
	}

	numVersions := len(versions)
	if numVersions == 0 {
		logger.Info("Nothing to build; all versions already exist in target repo")
		return nil
	}

	// Build directory tree and generate Dockerfile from template for each version
	logger.Info("Building and uploading images")

	// Persist tags to cache file and cleanup build directories
	var pathsToCleanup []string
	defer func() {
		// Save tag cache; it's deferred as the main loop might alter the tags
		logger.Debug("Saving tag cache")
		if err = saveTagCache(cachePath, tags); err != nil {
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

		logger.Debugf("Processing tag %d/%d: %s", idx+1, numVersions, version)

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
		buildTags := []string{imageTag}
		if opts.TagLatest && idx == numVersions-1 {
			buildTags = append(buildTags, fmt.Sprintf("%s:%s", opts.TargetRepo, "latest"))
			logger.Infof("Tagging image %s as latest", imageTag)
		}

		// Build image
		logger.Infof("Building image %s", imageTag)
		imageID, baseID, err := client.Images().Build(ctx, buildDirectory, buildTags...)
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
			err = client.Images().Push(ctx, buildTags...)
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

	// Clean up last iteration's images
	finalCleanup := make([]string, 0, 2)
	if previousImage != "" {
		finalCleanup = append(finalCleanup, previousImage)
	}
	if previousBaseImage != "" {
		finalCleanup = append(finalCleanup, previousBaseImage)
	}
	if len(finalCleanup) > 0 {
		logger.Info("Removing final build artifacts")
		if err = client.Images().Remove(ctx, finalCleanup...); err != nil {
			logger.Errorf("Failed to remove final images: %v", err)
			// Don't return error here; the main work is done
		}
	}

	return nil
}

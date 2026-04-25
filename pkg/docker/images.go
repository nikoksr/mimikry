package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/moby/go-archive"
	docker "github.com/moby/moby/client"
	"github.com/nikoksr/simplog"
	"github.com/rs/xid"
)

type ErrorDetail struct {
	Message string `json:"message"`
}

type ErrorLine struct {
	Error       string      `json:"error"`
	ErrorDetail ErrorDetail `json:"errorDetail"`
}

func (c *imageClient) getImageIDAndBaseID(ctx context.Context, imageRef string) (string, string, error) {
	dockerClient := c.client.dockerClient

	// Get image id
	imageListResult, err := dockerClient.ImageList(ctx, docker.ImageListOptions{
		Filters: make(docker.Filters).Add("reference", imageRef),
	})
	if err != nil {
		return "", "", fmt.Errorf("list images: %w", err)
	}

	if len(imageListResult.Items) == 0 {
		return "", "", fmt.Errorf("image %q not found", imageRef)
	}

	// Trim the sha256: prefix from the image id
	imageID := strings.TrimPrefix(imageListResult.Items[0].ID, "sha256:")

	// Get base image id. The base image ID is the last entry in the image history that is not <missing>.

	// Get image history
	historyResult, err := dockerClient.ImageHistory(ctx, imageID)
	if err != nil {
		return "", "", fmt.Errorf("get image history: %w", err)
	}

	// Find the base image id
	baseID := ""
	for _, history := range historyResult.Items {
		if history.ID == "<missing>" {
			continue
		}

		baseID = strings.TrimPrefix(history.ID, "sha256:")
	}

	if baseID == "" {
		return "", "", fmt.Errorf("could not find base image id for %q", imageRef)
	}

	return imageID, baseID, nil
}

// Build builds a docker image from the given build directory. It returns the image ID and the base (parent) image ID.
// BuildKit is currently disabled due to compatibility issues.
func (c *imageClient) Build(ctx context.Context, buildDir string, tags ...string) (string, string, error) {
	logger := simplog.FromContext(ctx)
	dockerClient := c.client.dockerClient

	if len(tags) == 0 {
		return "", "", errors.New("no tags provided")
	}

	// Create Build Context
	buildContext, err := archive.TarWithOptions(buildDir, &archive.TarOptions{
		IncludeFiles: []string{"."},
	})
	if err != nil {
		return "", "", fmt.Errorf("create build context: %w", err)
	}

	// Build Configuration
	buildOptions := docker.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       tags,
		BuildArgs:  map[string]*string{},
		BuildID:    xid.New().String(),
		Remove:     true,
		// FIXME: Enabling BuildKit causes the build to fail
		// Version: docker.BuilderBuildKit,
	}

	// Build Image
	logger.Debugf("Starting build for %v", tags)

	buildResponse, err := dockerClient.ImageBuild(ctx, buildContext, buildOptions)
	if err != nil {
		return "", "", fmt.Errorf("build image: %w", err)
	}

	// Parse the build output for errors
	var errLines []string
	scanner := bufio.NewScanner(buildResponse.Body)
	for scanner.Scan() {
		line := scanner.Text()
		logger.Debug(line)

		// Parse each line and look for errors
		errLine := &ErrorLine{}
		if err := json.Unmarshal([]byte(line), errLine); err == nil && errLine.Error != "" {
			errLines = append(errLines, errLine.ErrorDetail.Message)
		}
	}

	// Close the build response body
	_ = buildResponse.Body.Close()

	// Check if any errors were captured during build
	if len(errLines) > 0 {
		return "", "", fmt.Errorf("build image: %w", errors.New(strings.Join(errLines, "; ")))
	}

	logger.Debugf("Build finished for %v", tags)

	// Get image id
	imageID, baseID, err := c.getImageIDAndBaseID(ctx, tags[0])
	if err != nil {
		return "", "", fmt.Errorf("get image id: %w", err)
	}

	return imageID, baseID, nil
}

// Push pushes a docker image to a registry. It calls the docker cli command.
func (c *imageClient) Push(ctx context.Context, images ...string) error {
	logger := simplog.FromContext(ctx)
	dockerClient := c.client.dockerClient
	authToken := c.client.authToken

	for _, imageRef := range images {
		err := func() error {
			logger.Debugf("Pushing image %q", imageRef)

			options := docker.ImagePushOptions{
				RegistryAuth: authToken,
			}
			response, err := dockerClient.ImagePush(ctx, imageRef, options)
			if err != nil {
				return err
			}
			defer response.Close()

			scanner := bufio.NewScanner(response)
			for scanner.Scan() {
				line := scanner.Text()
				logger.Debug(line)

				// Parse each line and look for errors
				errLine := &ErrorLine{}
				if err := json.Unmarshal([]byte(line), errLine); err == nil && errLine.Error != "" {
					return fmt.Errorf("push image %q: %s", imageRef, errLine.ErrorDetail.Message)
				}
			}

			return nil
		}()
		if err != nil {
			return err
		}
	}

	return nil
}

// Remove removes one or more docker images. It returns an error if one of the images could not be removed. It uses
// the docker API.
func (c *imageClient) Remove(ctx context.Context, ids ...string) error {
	logger := simplog.FromContext(ctx)
	dockerClient := c.client.dockerClient

	for _, id := range ids {
		result, err := dockerClient.ImageRemove(ctx, id, docker.ImageRemoveOptions{
			Force:         true,
			PruneChildren: true,
		})
		if err != nil {
			return fmt.Errorf("remove image %q: %w", id, err)
		}

		for _, response := range result.Items {
			logger.Debugf("Removed image %v", response)
		}
	}

	return nil
}

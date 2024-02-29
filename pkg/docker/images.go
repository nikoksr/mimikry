package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/archive"
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

func (c *imageClient) getImageIDAndBaseID(ctx context.Context, image string) (string, string, error) {
	client := c.provider.GetDockerClient()

	// Get image id
	imageList, err := client.ImageList(ctx, types.ImageListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", image)),
	})
	if err != nil {
		return "", "", fmt.Errorf("list images: %w", err)
	}

	if len(imageList) == 0 {
		return "", "", fmt.Errorf("image %q not found", image)
	}

	// Trim the sha256: prefix from the image id
	imageID := strings.TrimPrefix(imageList[0].ID, "sha256:")

	// Get base image id. The base image ID is the last entry in the image history that is not <missing>.

	// Get image history
	imageHistory, err := client.ImageHistory(ctx, imageID)
	if err != nil {
		return "", "", fmt.Errorf("get image history: %w", err)
	}

	// Find the base image id
	baseID := ""
	for _, history := range imageHistory {
		if history.ID == "<missing>" {
			continue
		}

		baseID = strings.TrimPrefix(history.ID, "sha256:")
	}

	if baseID == "" {
		return "", "", fmt.Errorf("could not find base image id for %q", image)
	}

	return imageID, baseID, nil
}

// Build builds a docker image from a dockerfile. It returns the image ID and an error. It calls the docker cli command.
// The build command is run with BuildKit enabled.
func (c *imageClient) Build(ctx context.Context, buildDir string, tags ...string) (string, string, error) {
	logger := simplog.FromContext(ctx)
	client := c.provider.GetDockerClient()

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
	buildOptions := types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       tags,
		BuildArgs:  map[string]*string{},
		BuildID:    xid.New().String(),
		Remove:     true,
		// FIXME: Enabling BuildKit causes the build to fail
		// Version: types.BuilderBuildKit,
	}

	// Build Image
	logger.Debugf("Starting build for %v", tags)

	buildResponse, err := client.ImageBuild(ctx, buildContext, buildOptions)
	if err != nil {
		return "", "", fmt.Errorf("build image: %w", err)
	}

	// Parse the build output for errors
	errLines := make([]string, 0)
	scanner := bufio.NewScanner(buildResponse.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if false {
			logger.Debug(line)
		}

		// Parse each line and look for errors
		errLine := &ErrorLine{}
		if err := json.Unmarshal([]byte(line), errLine); err == nil && errLine.Error != "" {
			errLines = append(errLines, errLine.ErrorDetail.Message)
		}
	}

	// Close the build response body
	_ = buildResponse.Body.Close()

	prettyBuildResponse, _ := json.MarshalIndent(buildResponse, "", "  ")
	logger.Debugf("Build response: %s", string(prettyBuildResponse))

	// Check if any errors were captured during build
	if len(errLines) > 0 {
		return "", "", fmt.Errorf("build image: %w", errors.New(strings.Join(errLines, "; ")))
	}

	logger.Debugf("Build finished for %v", tags)

	// Get image id
	imageID, parentID, err := c.getImageIDAndBaseID(ctx, tags[0])
	if err != nil {
		return "", "", fmt.Errorf("get image id: %w", err)
	}

	return imageID, parentID, nil
}

// Push pushes a docker image to a registry. It calls the docker cli command.
func (c *imageClient) Push(ctx context.Context, images ...string) error {
	logger := simplog.FromContext(ctx)
	client := c.provider.GetDockerClient()
	authToken := c.provider.GetAuthToken()

	for _, image := range images {
		err := func() error {
			logger.Debugf("Pushing image %q", image)

			options := types.ImagePushOptions{
				RegistryAuth: authToken,
			}
			response, err := client.ImagePush(ctx, image, options)
			if err != nil {
				return err
			}
			defer response.Close()

			scanner := bufio.NewScanner(response)
			for scanner.Scan() {
				line := scanner.Text()
				logger.Debug(line)
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
	client := c.provider.GetDockerClient()

	for _, id := range ids {
		responses, err := client.ImageRemove(ctx, id, types.ImageRemoveOptions{
			Force:         true,
			PruneChildren: true,
		})
		if err != nil {
			return fmt.Errorf("remove image %q: %w", id, err)
		}

		for _, response := range responses {
			logger.Debugf("Removed image %v", response)
		}
	}

	return nil
}

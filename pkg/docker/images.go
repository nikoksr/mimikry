package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/archive"
	"github.com/nikoksr/simplog"
)

type ErrorLine struct {
	Error       string      `json:"error"`
	ErrorDetail ErrorDetail `json:"errorDetail"`
}

type ErrorDetail struct {
	Message string `json:"message"`
}

func (c *imageClient) getID(ctx context.Context, image string) (string, error) {
	client := c.provider.GetDockerClient()

	images, err := client.ImageList(ctx, types.ImageListOptions{
		Filters: filters.NewArgs(filters.Arg(
			"reference", image,
		)),
	})
	if err != nil {
		return "", fmt.Errorf("list images: %w", err)
	}
	if len(images) == 0 {
		return "", fmt.Errorf("no image found for %q", image)
	}

	return images[0].ID, nil
}

// Build builds a docker image from a dockerfile. It returns the image ID and an error. It calls the docker cli command.
// The build command is run with BuildKit enabled.
func (c *imageClient) Build(ctx context.Context, buildDir, tag string) (string, error) {
	logger := simplog.FromContext(ctx)

	client := c.provider.GetDockerClient()
	clientVersion := client.ClientVersion()
	serverVersion, err := client.ServerVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("get server version: %w", err)
	}

	prettyServerVersion, _ := json.MarshalIndent(serverVersion, "", "  ")

	logger.Debugf("Building image using docker client version %s and docker server version %s", clientVersion, prettyServerVersion)

	// Create Build Context
	buildContext, err := archive.TarWithOptions(buildDir, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("create build context: %w", err)
	}

	// Build Configuration
	buildOptions := types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{tag},
		Remove:     true,
		// FIXME: Enabling BuildKit causes the build to fail
		Version: types.BuilderBuildKit,
	}

	// Build Image
	buildResponse, err := client.ImageBuild(context.Background(), buildContext, buildOptions)
	if err != nil {
		return "", fmt.Errorf("build image: %w", err)
	}

	// Close the response to prevent leaks
	defer func() { _ = buildResponse.Body.Close() }()

	var lastLine string
	scanner := bufio.NewScanner(buildResponse.Body)
	for scanner.Scan() {
		lastLine = scanner.Text()
		logger.Debug(lastLine)
	}
	errLine := &ErrorLine{}
	_ = json.Unmarshal([]byte(lastLine), errLine)
	if errLine.Error != "" {
		return "", fmt.Errorf("build image: %w", errors.New(errLine.ErrorDetail.Message))
	}

	logger.Infof("Successfully built image %q", buildResponse.OSType)

	// Get image id
	imageID, err := c.getID(ctx, tag)
	if err != nil {
		return "", fmt.Errorf("get image id: %w", err)
	}

	return imageID, nil
}

// Tag tags an image with the given tags.
func (c *imageClient) Tag(ctx context.Context, source string, targets ...string) error {
	client := c.provider.GetDockerClient()
	for _, target := range targets {
		if err := client.ImageTag(ctx, source, target); err != nil {
			return fmt.Errorf("tag image %q as %q: %w", source, target, err)
		}
	}

	return nil
}

// Push pushes a docker image to a registry. It calls the docker cli command.
func (c *imageClient) Push(ctx context.Context, images ...string) error {
	client := c.provider.GetDockerClient()
	authToken := c.provider.GetAuthToken()
	for _, image := range images {
		func() {
			options := types.ImagePushOptions{
				RegistryAuth: authToken,
			}
			response, err := client.ImagePush(ctx, image, options)
			if err != nil {
				panic(err)
			}
			defer response.Close()
		}()
	}

	return nil
}

// Remove removes one or more docker images. It returns an error if one of the images could not be removed. It uses
// the docker API.
func (c *imageClient) Remove(ctx context.Context, ids ...string) error {
	client := c.provider.GetDockerClient()
	for _, id := range ids {
		_, err := client.ImageRemove(ctx, id, types.ImageRemoveOptions{
			Force: true,
		})
		if err != nil {
			return fmt.Errorf("remove image %q: %w", id, err)
		}
	}

	return nil
}

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver"
	"github.com/cockroachdb/errors"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/nikoksr/simplog"
)

type (
	// ImageClient is a client for docker images. It is used to build, tag, push and remove docker images.
	ImageClient interface {
		Build(ctx context.Context, dockerfile, tag string) (string, error)
		Tag(ctx context.Context, source string, targets ...string) error
		Push(ctx context.Context, images ...string) error
		Remove(ctx context.Context, ids ...string) error
	}

	// Client is the main docker client. It is used to create other clients.
	Client struct {
		client *client.Client
		Image  ImageClient
	}

	imageClient struct {
		client *client.Client
	}

	registryTagsResponse struct {
		Next    string `json:"next"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
)

// Compile-time check if imageClient implements ImageClient.
var _ ImageClient = (*imageClient)(nil)

var (
	initClientOnce sync.Once
	stdClient      *Client

	patternImageTag        = regexp.MustCompile(`^\d+\.\d+$`)
	patternRegistryTagsURL = "https://registry.hub.docker.com/v2/repositories/library/%s/tags?page=1&page_size=%d"

	registryAPIPageLimit = 100
)

func getTags(ctx context.Context, url string) ([]*semver.Version, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", errors.Wrap(err, "create request")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", errors.Wrap(err, "do request")
	}
	defer resp.Body.Close()

	var registryResponse registryTagsResponse
	err = json.NewDecoder(resp.Body).Decode(&registryResponse)
	if err != nil {
		return nil, "", err
	}

	var tags []*semver.Version
	for _, result := range registryResponse.Results {
		if patternImageTag.MatchString(result.Name) {
			tag, err := semver.NewVersion(result.Name)
			if err != nil {
				return nil, "", err
			}

			tags = append(tags, tag)
		}
	}

	return tags, registryResponse.Next, nil
}

func getAllTags(ctx context.Context, repo string) ([]*semver.Version, error) {
	var tags []*semver.Version

	next := fmt.Sprintf(patternRegistryTagsURL, repo, registryAPIPageLimit)
	for next != "" {
		var err error
		var newTags []*semver.Version
		newTags, next, err = getTags(ctx, next)
		if err != nil {
			return nil, err
		}

		tags = append(tags, newTags...)
	}

	sort.Sort(semver.Collection(tags))

	return tags, nil
}

// GetDockerHubRepoTags returns all tags for the given docker hub repository. The resulting list gets sorted in
// ascending order. Currently, the default behavior is to only return tags that match the pattern \d+\.\d+.
func GetDockerHubRepoTags(ctx context.Context, repo string) ([]*semver.Version, error) {
	return getAllTags(ctx, repo)
}

// FullTag returns the full tag for the given image and tag.
func FullTag(image string, tag any) string {
	return fmt.Sprintf("%s:%v", image, tag)
}

func newClient() (*Client, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, err
	}

	return &Client{
		client: docker,
		Image:  &imageClient{client: docker},
	}, nil
}

// New returns a new docker client.
func New(ctx context.Context) (*Client, error) {
	logger := simplog.FromContext(ctx)

	var err error
	initClientOnce.Do(func() {
		logger.Debug("create new docker client")
		stdClient, err = newClient()
	})
	if err != nil {
		return nil, err
	}

	// Ping the docker daemon to check if it is running.
	logger.Debug("ping docker daemon")
	resp, err := stdClient.client.Ping(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "ping docker daemon")
	}
	logger.Debugf("docker daemon responded with: %+v", resp)

	return stdClient, nil
}

// Login logs in to the docker registry using the given auth config. It uses the docker CLI to login.
func (c *Client) Login(ctx context.Context, auth types.AuthConfig) error {
	logger := simplog.FromContext(ctx)
	logger.Debug("login to docker registry")

	// TODO: Solve this
	//nolint:gosec // Ignoring this for now, didn't find a solution on the spot.
	cmd := exec.CommandContext(ctx, "docker", "login", "-u", auth.Username, "--password-stdin")
	cmd.Stdin = strings.NewReader(auth.Password)

	return cmd.Run()
}

// LoginBasic logs in to the docker registry using the given username and password. It calls the Login method internally.
func (c *Client) LoginBasic(ctx context.Context, username, password string) error {
	return c.Login(ctx, types.AuthConfig{Username: username, Password: password})
}

// LoginFromEnv logs in to the docker registry using the environment variables DOCKER_USERNAME and DOCKER_PASSWORD. It
// calls the LoginBasic method internally.
func (c *Client) LoginFromEnv(ctx context.Context) error {
	return c.LoginBasic(ctx, os.Getenv("DOCKER_USERNAME"), os.Getenv("DOCKER_PASSWORD"))
}

// Logout logs out of the docker registry. This is a no-op if the client is not logged in.
func (c *Client) Logout(ctx context.Context) error {
	logger := simplog.FromContext(ctx)
	logger.Debug("logging out of docker registry")

	cmd := exec.CommandContext(ctx, "docker", "logout")

	return cmd.Run()
}

// Close closes the client.
func (c *Client) Close(_ context.Context) error {
	return c.client.Close()
}

func (c *imageClient) getID(ctx context.Context, image string) (string, error) {
	images, err := c.client.ImageList(ctx, types.ImageListOptions{
		Filters: filters.NewArgs(filters.Arg(
			"reference", image,
		)),
	})
	if err != nil {
		return "", err
	}
	if len(images) == 0 {
		return "", errors.Errorf("image %q not found", image)
	}

	return images[0].ID, nil
}

// Build builds a docker image from a dockerfile. It returns the image ID and an error. It calls the docker cli command.
// The build command is run with BuildKit enabled.
func (c *imageClient) Build(ctx context.Context, dockerfile, tag string) (string, error) {
	// Execute docker build in shell
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", tag, "-f", dockerfile, ".")

	// Add buildkit env for parallel build
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")

	err := cmd.Run()
	if err != nil {
		return "", errors.Wrapf(err, "build image %q from %q", tag, dockerfile)
	}

	// Get image id
	imageID, err := c.getID(ctx, tag)
	if err != nil {
		return "", errors.Wrapf(err, "get image id %q", tag)
	}

	return imageID, nil
}

func (c *imageClient) tag(ctx context.Context, source string, targets ...string) error {
	for _, target := range targets {
		err := c.client.ImageTag(ctx, source, target)
		if err != nil {
			return errors.Wrapf(err, "tag image %q as %q", source, target)
		}
	}

	return nil
}

// Tag tags an image with the given tags.
func (c *imageClient) Tag(ctx context.Context, source string, targets ...string) error {
	return c.tag(ctx, source, targets...)
}

func (c *imageClient) push(ctx context.Context, images ...string) error {
	for _, image := range images {
		cmd := exec.CommandContext(ctx, "docker", "push", image)
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "push image %q", image)
		}
	}

	return nil
}

// Push pushes a docker image to a registry. It calls the docker cli command.
func (c *imageClient) Push(ctx context.Context, images ...string) error {
	return c.push(ctx, images...)
}

func (c *imageClient) remove(ctx context.Context, ids ...string) error {
	for _, id := range ids {
		_, err := c.client.ImageRemove(ctx, id, types.ImageRemoveOptions{
			Force: true,
		})
		if err != nil {
			return errors.Wrapf(err, "remove image %q", id)
		}
	}

	return nil
}

// Remove removes one or more docker images. It returns an error if one of the images could not be removed. It uses
// the docker API.
func (c *imageClient) Remove(ctx context.Context, ids ...string) error {
	return c.remove(ctx, ids...)
}

package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/moby/moby/api/types/registry"
	docker "github.com/moby/moby/client"
	"github.com/nikoksr/simplog"
)

var _ ImageClient = (*imageClient)(nil)

type (
	// Client is the main docker client. It is used to create other clients.
	Client struct {
		dockerClient *docker.Client

		authToken string // base64 encoded auth config, used for registry operations. Gets set by Login methods.
	}

	// ImageClient is a client for docker images. It is used to build, tag, push and remove docker images.
	ImageClient interface {
		Build(ctx context.Context, buildDir string, tags ...string) (string, string, error)
		Push(ctx context.Context, images ...string) error
		Remove(ctx context.Context, ids ...string) error
	}

	// Actual implementation of ImageClient
	imageClient struct {
		client *Client
	}
)

func newClient() (*Client, error) {
	client, err := docker.NewClientWithOpts(docker.FromEnv, docker.WithAPIVersionNegotiation()) // nosec:SA1019 -- TODO: migrate to docker.New()
	if err != nil {
		return nil, err
	}

	return &Client{dockerClient: client}, nil
}

// New returns a new docker client.
func New(ctx context.Context) (*Client, error) {
	logger := simplog.FromContext(ctx)

	logger.Debug("create new docker client")
	client, err := newClient()
	if err != nil {
		return nil, err
	}

	// Ping the docker daemon to check if it is running.
	logger.Debug("ping docker daemon")
	resp, err := client.dockerClient.Ping(ctx, docker.PingOptions{})
	if err != nil {
		return nil, fmt.Errorf("ping docker daemon: %w", err)
	}

	logger.Debugf("docker daemon responded with: %+v", resp)

	return client, nil
}

// Images returns a client for building, pushing, and removing Docker images.
func (c *Client) Images() ImageClient {
	return &imageClient{client: c}
}

// GetDockerHubRepoTags returns all tags for the given docker hub repository. The resulting list gets sorted in
// ascending order. Currently, the default behavior is to only return tags that match the pattern \d+\.\d+.
func GetDockerHubRepoTags(ctx context.Context, repo string) ([]string, error) {
	return getAllTags(ctx, repo)
}

// Login logs in to the docker registry using the given auth config. It uses the docker CLI to login.
func (c *Client) Login(ctx context.Context, auth registry.AuthConfig) error {
	logger := simplog.FromContext(ctx)

	logger.Debug("Verifying docker login")
	if auth.Username == "" || auth.Password == "" {
		return fmt.Errorf("docker login failed: username or password is empty")
	}

	logger.Debugf("Logging in to docker registry as %s", auth.Username)

	// Registry Login
	loginResult, err := c.dockerClient.RegistryLogin(ctx, docker.RegistryLoginOptions{
		Username: auth.Username,
		Password: auth.Password,
	})
	if err != nil {
		return fmt.Errorf("login to docker registry: %w", err)
	}

	authResponse := loginResult.Auth
	logger.Debugf("login response: %+v", authResponse)

	// Set auth string
	c.authToken = authResponse.IdentityToken

	if c.authToken == "" {
		// If no token was returned, we need to create one from the auth config
		logger.Debug("no token returned, creating one from auth config")

		authBytes, err := json.Marshal(auth)
		if err != nil {
			return fmt.Errorf("marshal auth config: %w", err)
		}

		c.authToken = base64.StdEncoding.EncodeToString(authBytes)
	}

	return nil
}

// LoginFromEnv logs in to the docker registry using the environment variables DOCKER_USERNAME and DOCKER_PASSWORD. It
// calls the Login method internally.
func (c *Client) LoginFromEnv(ctx context.Context) error {
	return c.Login(ctx, registry.AuthConfig{
		Username: os.Getenv("DOCKER_USERNAME"),
		Password: os.Getenv("DOCKER_PASSWORD"),
	})
}

// Logout logs out of the docker registry. This is a no-op if the client is not logged in.
//
// NOTE: This is currently a no-op because the docker client does not support logging out. The login token is not persisted.
func (c *Client) Logout(ctx context.Context) error {
	logger := simplog.FromContext(ctx)
	logger.Debug("logging out of docker registry (noop)")

	return nil
}

// Close closes the client.
func (c *Client) Close(_ context.Context) error {
	return c.dockerClient.Close()
}

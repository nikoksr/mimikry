package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type registryTagsResponse struct {
	Next    string `json:"next"`
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
}

var (
	patternRegistryTagsURL = "https://registry.hub.docker.com/v2/repositories/library/%s/tags?page=1&page_size=%d"
	registryAPIPageLimit   = 100
)

func getTags(ctx context.Context, url string) ([]string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	var registryResponse registryTagsResponse
	if err = json.NewDecoder(resp.Body).Decode(&registryResponse); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}

	tags := make([]string, 0, len(registryResponse.Results))
	for _, result := range registryResponse.Results {
		tags = append(tags, result.Name)
	}

	return tags, registryResponse.Next, nil
}

func getAllTags(ctx context.Context, repo string) ([]string, error) {
	var tags []string

	next := fmt.Sprintf(patternRegistryTagsURL, repo, registryAPIPageLimit)
	for next != "" {
		var err error
		var newTags []string
		newTags, next, err = getTags(ctx, next)
		if err != nil {
			return nil, fmt.Errorf("get tags: %w", err)
		}

		tags = append(tags, newTags...)
	}

	return tags, nil
}

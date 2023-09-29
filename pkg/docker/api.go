package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"

	"github.com/Masterminds/semver"
)

type registryTagsResponse struct {
	Next    string `json:"next"`
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
}

var (
	patternImageTag        = regexp.MustCompile(`^\d+\.\d+$`)
	patternRegistryTagsURL = "https://registry.hub.docker.com/v2/repositories/library/%s/tags?page=1&page_size=%d"

	registryAPIPageLimit = 100
)

type SkipTagFunc func(tag string) bool

var stdSkipTagFunc = func(tag string) bool {
	return !patternImageTag.MatchString(tag)
}

func getTags(ctx context.Context, url string, skipTagFn SkipTagFunc) ([]*semver.Version, string, error) {
	if skipTagFn == nil {
		skipTagFn = stdSkipTagFunc
	}

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

	tags := make([]*semver.Version, 0, len(registryResponse.Results))
	for _, result := range registryResponse.Results {
		if skipTagFn(result.Name) {
			continue
		}

		tag, err := semver.NewVersion(result.Name)
		if err != nil {
			return nil, "", err
		}

		tags = append(tags, tag)
	}

	return tags, registryResponse.Next, nil
}

func getAllTags(ctx context.Context, repo string, skipTagFn SkipTagFunc) ([]*semver.Version, error) {
	var tags []*semver.Version

	next := fmt.Sprintf(patternRegistryTagsURL, repo, registryAPIPageLimit)
	for next != "" {
		var err error
		var newTags []*semver.Version
		newTags, next, err = getTags(ctx, next, skipTagFn)
		if err != nil {
			return nil, fmt.Errorf("get tags: %w", err)
		}

		tags = append(tags, newTags...)
	}

	sort.Sort(semver.Collection(tags))

	return tags, nil
}

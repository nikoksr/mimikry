# Mimikry

Mimikry is a tool to mimic a docker registry repository. For now, it serves a very specific purposes and is not abstracted enough for general usage, so please be aware of that.

## Disclaimer

The current stage of the project is a proof of concept, it's as early stage as it gets. It is not production ready and should not be used in production. The plan is however to build out its features and abstract away my current specific use cases.

## How it works

- Load all available tags from the parent image using the docker registry API
- Filter out tags that are not semver compatible
- Match against (optional) semver constraint
- For each remaining tag:
  - Compile the provided templates (the only required template is `Dockerfile`, others are optional) with the current tag (more dynamic data can be added in the future)
  - Build an image based on the compiled Dockerfile template
  - Push the image to the docker registry (if not in dry-run mode)

## Usage

```bash
# Build all versions for parent image of Dockerfile template and push them to the given docker repo
mimikry my-templates/ johndoe/some-repo

# Only build version 12.3 for parent image of Dockerfile template and push it to the given docker repo
mimikry -v "12.3" my-templates/ johndoe/some-repo

# Build versions that are greater than or equal to 12.3 for parent image of Dockerfile template and push them to the given docker repo
mimikry -v ">= 12.3" my-templates/ johndoe/some-repo

# Build versions that are greater than or equal to 12.0 and less than 13.0 for parent image of Dockerfile template and push them to the given docker repo and tag the latest image
mimikry -v "^12" --latest my-templates/ johndoe/some-repo

# For more info about version constraints, read here: https://github.com/Masterminds/semver?tab=readme-ov-file#basic-comparisons
```

> Note: For more, check the help section of the `mimikry`: `mimikry --help`

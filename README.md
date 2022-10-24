# Mimikry

Mimikry is a tool to mimic a docker registry repository. For now, it serves a very specific purposes and is not abstracted enough for general usage, so please be aware of that.

## Disclaimer

The current stage of the project is a proof of concept, it's as early stage as it gets. It is not production ready and should not be used in production. The plan is however to build out its features and abstract away my current specific use cases.

## How it works

Mimikry's functionality is based on a provided Go-template file of a Dockerfile. This template is used to generate a Dockerfile for each tag of the repository.

Mimikry will load all semver tags from the parent repository and generate a Dockerfile for each tag. The generated Dockerfile will be built and pushed to the provided target repository. An example Dockerfile template can be found in the [examples](examples) directory.

## Usage

Mimikry is a CLI tool and can be used as follows:

```bash
# Build all versions for parent image of Dockerfile template and push them to docker repo "johndoe/some-image"
mimikry -m "John Doe" Dockerfile.tmlp johndoe/some-image

# Only build version 12.3 for parent image of Dockerfile template and push it to docker repo "johndoe/some-image"
mimikry -m "John Doe" -v 12.3 Dockerfile.tmlp johndoe/some-image
```


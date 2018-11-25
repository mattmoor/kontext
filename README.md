# Kontext

Hello there. Once again.

## Overview

This repository contains an EXPERIMENTAL PROTOTYPE, with the goal of
uploading a local source context (e.g. a local directory) for use in
the Knative Build CRD (or compatible definitions).

The high-level objectives of this prototype are:
1. Aim for a level of incrementality akin to the "GCS Fetcher" currently
  available in the Knative Build CRD,
1. Work through vendor-agnostic / standardized APIs,
1. Don't require implementation-specific knowledge of how the Build CRD works.

### How it works (tl;dr)

This tool works by essentially creating a self-extracting source-context, which
when run expands the files collected from the local directory into `/workspace`.

### How it works (detail)

Similar to [`ko`](https://github.com/google/go-containerregistry/blob/master/cmd/ko/README.md),
this tool assembles a container image directly (**without Docker**), which effectively
looks like:

```Dockerfile
# This effectively does a: cp -r /var/run/kontext /workspace
FROM <image pre-built from ./cmd/extractor>

# Add the directory containing the desired context under this path.
ADD ./dir /var/run/kontext

# Add a manifest of the files included (more on this later)
ADD <manifest> /var/lib/kontext/manifest.json

```

So without Docker, we effectively assemble the source directory into a container
image, which can be done by invoking:

```
go run ./cmd/kontext/main.go --directory=/path/to/directory --tag=gcr.io/project/image:tag
```

This can then be supplied to the Build CRD's custom source step like:

```yaml
spec:
  source:
    custom:
      image: gcr.io/project/image:tag
```

Or tested locally via:

```
docker pull gcr.io/project/image:tag
docker run -ti --rm -v /path/on/host:/workspace gcr.io/project/image:tag
```

### How it is incremental

On subsequent invocations, `kontext` first cracks open the target image (if it exists)
and extracts the manifest file, which reflects the state of the `/var/run/kontext`
directory once mounted by an overlayfs.  Leveraging this context, it can compute a
"delta" layer, which only contains changed files.  For example:

```Dockerfile
FROM <previous iteration of the target image>

# Based on the manifest of the previous image, a single layer
# is added that adds any changed files, and "whites out" any
# files/directories that have been removed.
ADD <ONLY CHANGED FILES> /var/run/kontext

# Add a new manifest of the files included.
ADD <manifest> /var/lib/kontext/manifest.json

```

So for example, if the directory being uploaded as context is:

```
a/
  b
  c       <-- new
  d

foo/
  bar     <-- new

baz/
  blah/
    bleh  <-- changed

gone/
  ...     <-- all removed.
```

Then the "delta" layer would only contain:

```
a/
  c

foo/
  bar

baz/
  blah/
    bleh

.wh.gone  <-- "whiteout" file
```


## TODOs

TODO(#11): Blah blah

* Handle layer limits
* Handle symlinks
* Heuristics around whether to reuse the previous image at all
BLAH
BLAH
BLAH

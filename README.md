# splitfs

A filesystem that chunks files up.

## What does it do?

`splitfs` takes a directory and mirrors it on another. However, all files within that directory will be presented as directories themselves, containing one or more "chunk files" which each correspond to a portion of the original file.

For example:

```shell
$ tree /testdata
/testdata
├── 10KB. (size=10KiB)
├── 20KB.data (size=20KiB)
├── Andy_Mabbett_-_RSC_-_How_to_Edit_Wikipedia_-_01_-_italic_bold.webm (size=7.5M)
├── file-sources.txt (size=262 bytes)
├── Flower-300x300_dtf.jpg (size=168K)
└── subdir
    ├── 20KB.symlink -> ../20KB.data
    └── 40KB.data (size=40KiB)

1 directory, 7 files

$ splitfs --chunk_size=10KiB ./testdata /mnt

$ tree /mnt
/mnt
├── 10KB.data
│   └── 71da741724c3f289_00000001_of_00000001.splitfs.chunk
├── 20KB.data
│   ├── 1b15a91efc959b04_00000001_of_00000002.splitfs.chunk
│   └── 1b15a91efc959b04_00000002_of_00000002.splitfs.chunk
├── Andy_Mabbett_-_RSC_-_How_to_Edit_Wikipedia_-_01_-_italic_bold.webm
│   ├── 764cc4cecb7e72ce_00000001_of_00000765.splitfs.chunk
│   ├── 764cc4cecb7e72ce_00000002_of_00000765.splitfs.chunk
│   │   ...
│   ├── 764cc4cecb7e72ce_00000764_of_00000765.splitfs.chunk
│   └── 764cc4cecb7e72ce_00000765_of_00000765.splitfs.chunk
├── file-sources.txt
│   └── 74c420ec46a3845a_00000001_of_00000001.splitfs.chunk
├── Flower-300x300_dtf.jpg
│   ├── efa4bffab14f7017_00000001_of_00000017.splitfs.chunk
│   ├── efa4bffab14f7017_00000002_of_00000017.splitfs.chunk
│   │   ...
│   ├── efa4bffab14f7017_00000016_of_00000017.splitfs.chunk
│   └── efa4bffab14f7017_00000017_of_00000017.splitfs.chunk
└── subdir
    ├── 20KB.symlink -> ../20KB.data
    └── 40KB.data
        ├── 36d928335f3367da_00000001_of_00000004.splitfs.chunk
        ├── 36d928335f3367da_00000002_of_00000004.splitfs.chunk
        ├── 36d928335f3367da_00000003_of_00000004.splitfs.chunk
        └── 36d928335f3367da_00000004_of_00000004.splitfs.chunk

8 directories, 790 files
```

**Note**: The chunked filesystem is read-only.

## Why?

Think of it as a filesystem-wide `split(1)`. Some use cases:

* You want to do more efficient backups of large sparse files for which only some random parts change at a time.
* You want to back up large files to a service whose API only lets you upload one file at a time in a non-resumable fashion.
* You want more efficient redundant copy detection for append-only files (you need to turn off total chunk counts and mtimes in filenames for this).
* You want to `split` a lot of files in a large directory structure but don't want to try hacking up a recursive shell loop to do it.

## Downloading and building from source

Because `go get` uses `https` to download Git repositories, while `perot.me` only serves them over the `git://` protocol, you have to manually fetch the repository in the right place.

```shell
# (if you haven't defined `GOPATH`, Go defaults to `GOPATH=~/go`)
$ export GOPATH="$HOME/go"

$ mkdir -p "$GOPATH/src/perot.me"
$ git clone git://perot.me/splitfs "$GOPATH/src/perot.me/splitfs"
$ go get -v perot.me/splitfs
$ go build  perot.me/splitfs

$ ./splitfs
Usage of splitfs:
  splitfs [options] <source directory> <target mountpoint>
[...]
```

## Usage

```shell
splitfs [flags] <source_directory> <mountpoint>
```

### Flags

* `chunk_size`: The size of each chunk. Must be suffixed by a unit (`B`, `KiB`, `MiB`, `GiB`, `TiB`). Default is `32MiB`.
* `exclude_regexp`: If specified, files with their full path (rooted at the source directory) match this regular expressions will show up as regular files in the mountpoint, rather than getting chunked.
* `filename_hash`: Algorithm for filename hashes in chunked filenames.
* `filename_includes_total_chunks`: Controls whether or not chunk filenames will contain the total number of chunks of the overall file.
* `filename_includes_mtime`: Controls whether or not chunk filenames will contain the mtime of the overall file.

## How do I get my files back from chunks?

For a one-off, just use `cat`:

```shell
$ cat /mnt/Flower-300x300_dtf.jpg/*.splitfs.chunk > /tmp/reconstituted.jpg

$ sha1sum /testdata/Flower-300x300_dtf.jpg /tmp/reconstituted.jpg
43e31dc3b3c541cf266d678b4309f73ca4d12cb6  /testdata/Flower-300x300_dtf.jpg
43e31dc3b3c541cf266d678b4309f73ca4d12cb6  /tmp/reconstituted.jpg
```

For more than just a one-off, wait until I implement `unsplitfs`, or do it yourself and send a pull request.

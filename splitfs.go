package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"perot.me/splitfs/hashes"
	"perot.me/splitfs/split"
)

var progName = filepath.Base(os.Args[0])

func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", progName)
	fmt.Fprintf(os.Stderr, "  %s [options] <source directory> <target mountpoint>\n", progName)
	flag.PrintDefaults()
}

// parseChunkSize parses a chunk size string into its value in bytes.
func parseChunkSize(chunkSize string) (int64, error) {
	units := map[string]int64{
		"B":   1,
		"KiB": 1 << 10,
		"MiB": 1 << 20,
		"GiB": 1 << 30,
		"TiB": 1 << 40,
	}
	unitsOrder := []string{"TiB", "GiB", "MiB", "KiB", "B"}
	for _, unit := range unitsOrder {
		if !strings.HasSuffix(chunkSize, unit) {
			continue
		}
		amountString := strings.TrimSuffix(chunkSize, unit)
		amount, err := strconv.Atoi(amountString)
		if err != nil {
			return 0, fmt.Errorf("%q is not an integer", amountString)
		}
		return int64(amount) * units[unit], nil
	}
	return 0, errors.New("no unit specified")
}

func main() {
	log.SetFlags(0)
	log.SetPrefix(fmt.Sprintf("%s: ", progName))
	flag.Usage = usage
	chunkSizeFlag := flag.String("chunk_size", "32MiB", "Chunk size. Available units: B, KiB, MiB, GiB, TiB.")
	excludeRegexpFlag := flag.String("exclude_regexp", "", "If specified, files with paths matching this regex (rooted at the source directory) will be reflected as plain, non-split files in the mountpoint. The regex is not full-match; use ^ and $ to make it so.")
	filenameHashFlag := flag.String("filename_hash", "sha256-b32", fmt.Sprintf("Algorithm for filename hashes in chunked filenames. Options: %v", hashes.HashNames))
	filenameIncludesTotalChunksFlag := flag.Bool("filename_includes_total_chunks", true, "Whether or not chunk filenames will contain the total number of chunks of the overall file.")
	filenameIncludesMtimeFlag := flag.Bool("filename_includes_mtime", false, "Controls whether or not chunk filenames will contain the mtime of the overall file.")
	pprofHostPortFlag := flag.String("pprof_host_port", "", "If specified, bind to this 'host:port'-formatted string and export pprof HTTP handlers on it. Useful for debugging.")
	flag.Parse()
	if flag.NArg() != 2 {
		usage()
		os.Exit(2)
	}
	sourceDirectory := flag.Arg(0)
	targetMountpoint := flag.Arg(1)
	if *pprofHostPortFlag != "" {
		go http.ListenAndServe(*pprofHostPortFlag, http.DefaultServeMux)
	}
	chunkSize, err := parseChunkSize(*chunkSizeFlag)
	if err != nil {
		log.Fatalf("Invalid chunk size %q: %v", *chunkSizeFlag, err)
	}
	hashFunc := hashes.GetHashFunc(*filenameHashFlag)
	if hashFunc == nil {
		log.Fatalf("Invalid hash function %q; must use one of %v", *filenameHashFlag, hashes.HashNames)
	}
	var options []split.Option
	if *excludeRegexpFlag != "" {
		options = append(options, split.ExcludeRegexp(*excludeRegexpFlag))
	}
	options = append(options, split.FilenameHashFunc(hashFunc))
	options = append(options, split.FilenameIncludesTotalChunks(*filenameIncludesTotalChunksFlag))
	options = append(options, split.FilenameIncludesMtime(*filenameIncludesMtimeFlag))
	splitFS, err := split.NewFS(sourceDirectory, int64(chunkSize), options...)
	if err != nil {
		log.Fatalf("Cannot initialize filesystem: %v", err)
	}
	fuseConn, err := fuse.Mount(
		targetMountpoint,
		fuse.FSName("splitfs"),
		fuse.LocalVolume(),
		fuse.VolumeName(fmt.Sprintf("splitfs %d %s", chunkSize, filepath.Base(sourceDirectory))))
	if err != nil {
		log.Fatalf("Cannot mount a filesystem at %q: %v", targetMountpoint, err)
	}
	defer fuseConn.Close()
	if err = fs.Serve(fuseConn, splitFS); err != nil {
		log.Fatalf("Cannot serve filesystem: %v", err)
	}
	<-fuseConn.Ready
	if err := fuseConn.MountError; err != nil {
		log.Fatal("Mount error: %v", err)
	}
}

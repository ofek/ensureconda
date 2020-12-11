package cmd

import (
	"archive/tar"
	"compress/bzip2"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wessie/appdirs"
	"github.com/flowchartsman/retry"
	"github.com/gofrs/flock"
	"github.com/hashicorp/go-version"
	"github.com/spf13/cobra"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	// Used for flags.

	rootCmd = &cobra.Command{
		Use:   "ensureconda",
		Short: "",
		Long:  ``,
		Run: func(cmd *cobra.Command, args []string) {
			mamba, err := evaluateFlagPair(cmd, "mamba")
			if err != nil {
				panic(err)
			}
			micromamba, err := evaluateFlagPair(cmd, "micromamba")
			if err != nil {
				panic(err)
			}
			conda, err := evaluateFlagPair(cmd, "conda")
			if err != nil {
				panic(err)
			}
			condaExe, err := evaluateFlagPair(cmd, "conda-exe")
			if err != nil {
				panic(err)
			}
			noInstall, err := cmd.Flags().GetBool("no-install")
			if err != nil {
				panic(err)
			}

			executable, err := EnsureConda(mamba, micromamba, conda, condaExe, true)
			if executable != "" {
				fmt.Print(executable)
				os.Exit(0)
			}
			if !noInstall {
				executable, err = EnsureConda(mamba, micromamba, conda, condaExe, noInstall)
				if err != nil {
					er(err)
				}
				if executable != "" {
					fmt.Print(executable)
					os.Exit(0)
				}
			}
			os.Exit(1)
		},
	}
)

func ResolveExecutable(executableName string, dataDir string) (string, error) {
	path := os.Getenv("PATH")
	defer os.Setenv("PATH", path)
	var filteredPaths []string
	// Append our special path first
	filteredPaths = append(filteredPaths, dataDir)

	for _, dir := range filepath.SplitList(path) {
		bad := filepath.Join(".pyenv", "shims")
		if !strings.Contains(dir, bad) {
			filteredPaths = append(filteredPaths, dir)
		}
	}
	newPathEnv := filepath.Join(filteredPaths...)
	os.Setenv("PATH", newPathEnv)
	return exec.LookPath(executableName)
}

func sitePath() string {
	return appdirs.UserDataDir("ensure-conda", "", "", false)
}

func EnsureConda(mamba bool, micromamba bool, conda bool, condaExe bool, noInstall bool) (string, error) {
	var executable string
	dataDir := sitePath()
	if mamba {
		executable, _ = ResolveExecutable("mamba", dataDir)
		if executable != "" {
			return executable, nil
		}
	}
	if micromamba {
		executable, _ = ResolveExecutable("micromamba", dataDir)
		if executable != "" {
			return executable, nil
		}
		if !noInstall {
			exe, err := InstallMicromamba()
			if err != nil {
				return "", err
			}
			return exe, nil
		}
	}
	if conda {
		// TODO: check $CONDA_EXE
		executable, _ = ResolveExecutable("conda", dataDir)
		if executable != "" {
			return executable, nil
		}
	}
	if condaExe {
		executable, _ = ResolveExecutable("conda_standalone", dataDir)
		if executable != "" {
			return executable, nil
		}
		if !noInstall {
			exe, err := InstallCondaStandalone()
			if err != nil {
				return "", err
			}
			return exe, nil

		}
	}

	return "", nil
}

type ArchSpec struct {
	os   string
	arch string
}

func PlatformSubdir() string {
	os_ := runtime.GOOS
	arch := runtime.GOARCH

	platformMap := make(map[ArchSpec]string)
	platformMap[ArchSpec{"darwin", "amd64"}] = "osx-64"
	platformMap[ArchSpec{"darwin", "arm64"}] = "osx-arm64"
	platformMap[ArchSpec{"linux", "amd64"}] = "linux-64"
	platformMap[ArchSpec{"linux", "arm64"}] = "linux-aarch64"
	platformMap[ArchSpec{"linux", "ppc64le"}] = "linux-ppc64le"
	platformMap[ArchSpec{"windows", "amd64"}] = "win-64"

	return platformMap[ArchSpec{os_, arch}]
}

func DownloadToFile(url string, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	retrier := retry.NewRetrier(10, 100*time.Millisecond, 5*time.Second)
	fileLock := flock.New(dst + ".lock")
	err = retrier.Run(func() error {
		locked, err := fileLock.TryLock()
		if err != nil {
			return err
		}
		if !locked {
			return errors.New("could not lock")
		}

		out, err := os.Create(dst)
		defer out.Close()

		_, err = io.Copy(out, resp.Body)
		return err
	})
	if err != nil {
		return err
	}

	st, _ := os.Stat(dst)
	err = os.Chmod(dst, st.Mode()|syscall.S_IXUSR)
	if err != nil {
		return err
	}
	return nil
}

//func InstallCondaStandalone() (string, error) {
//	condaExePrefix := "https://repo.anaconda.com/pkgs/misc/conda-execs"
//	subdir := PlatformSubdir()
//	condaExeName := fmt.Sprintf("conda-latest-%s.exe", subdir)
//
//	fileUrl := fmt.Sprintf("%s/%s", condaExePrefix, condaExeName)
//	targetFileName := targetExeFilename("conda_standalone")
//
//	err := DownloadToFile(fileUrl, targetFileName)
//	if err != nil {
//		return "", err
//	}
//	return targetFileName, nil
//}

func targetExeFilename(exeName string) string {
	_ = os.MkdirAll(sitePath(), 0700)
	targetFileName := filepath.Join(sitePath(), exeName)
	if runtime.GOOS == "windows" {
		targetFileName = targetFileName + ".exe"
	}
	return targetFileName
}

func InstallMicromamba() (string, error) {
	url := fmt.Sprintf("https://micromamba.snakepit.net/api/micromamba/%s/latest", PlatformSubdir())
	return installMicromamba(url)
}

type AnacondaPkgAttr struct {
	subdir       string
	version      string
	build_number int32
	timestamp    int32
	source_url   string
}

type AnacondaPkg struct {
	attrs AnacondaPkgAttr
}

type AnacondaPkgAttrs []AnacondaPkgAttr

func (a AnacondaPkgAttrs) Len() int { return len(a) }
func (a AnacondaPkgAttrs) Less(i, j int) bool {
	versioni, _ := version.NewVersion(a[i].version)
	versionj, _ := version.NewVersion(a[i].version)
	if versioni.LessThan(versionj) {
		return true
	} else if a[i].build_number < a[j].build_number {
		return true
	} else if a[i].timestamp < a[j].timestamp {
		return true
	}
	return false
}
func (a AnacondaPkgAttrs) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

func InstallCondaStandalone() (string, error) {
	// Get the most recent conda-standalone
	subdir := PlatformSubdir()
	url := "https://api.anaconda.org/package/anaconda/conda-standalone/files"
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		panic(err.Error())
	}

	var data []AnacondaPkg
	err = json.Unmarshal(body, &data)
	if err != nil {
		panic(err.Error())
	}

	var candidates = make([]AnacondaPkgAttr, 0)
	for _, datum := range data {
		if datum.attrs.subdir == subdir {
			candidates = append(candidates, datum.attrs)
		}
	}
	sort.Sort(AnacondaPkgAttrs(candidates))

	chosen := candidates[len(candidates)-1]

	installedExe, err := downloadAndUnpackCondaTarBz2(
		chosen.source_url, map[string]string{
			"stanalone_conda/conda.exe": targetExeFilename("conda_standalone"),
		})

	return installedExe, err
}

func downloadAndUnpackCondaTarBz2(
	url string,
	fileNameMap map[string]string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	bzf := bzip2.NewReader(resp.Body)
	tarReader := tar.NewReader(bzf)
	file, err := extractTarFiles(tarReader, fileNameMap)
	return file, err
}

func installMicromamba(url string) (string, error) {
	installedExe, err := downloadAndUnpackCondaTarBz2(
		url, map[string]string{
			"Library/bin/micromamba.exe": targetExeFilename("micromamba"),
			"bin/micromamba":             targetExeFilename("micromamba"),
		})

	return installedExe, err
}

func extractTarFiles(tarReader *tar.Reader, fileNameMap map[string]string) (string, error) {
	for true {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch header.Typeflag {
		case tar.TypeReg:
			targetFileName := fileNameMap[header.Name]
			if targetFileName != "" {
				err2 := extractTarFile(header, targetFileName, tarReader)
				if err2 != nil {
					return "", err2
				}
				return targetFileName, nil
			}
		}
	}
	return "", errors.New("could not find file in the tarball")
}

func extractTarFile(header *tar.Header, targetFileName string, tarReader *tar.Reader) error {
	fileInfo := header.FileInfo()
	r := retry.NewRetrier(10, 100*time.Millisecond, 5*time.Second)
	fileLock := flock.New(targetFileName + ".lock")

	err := r.Run(func() error {
		locked, err := fileLock.TryLock()
		if err != nil {
			return err
		}
		if !locked {
			return errors.New("could not lock")
		}

		file, err := os.OpenFile(targetFileName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileInfo.Mode().Perm())
		n, cpErr := io.Copy(file, tarReader)
		if err != nil {
			return err
		}
		if closeErr := file.Close(); closeErr != nil { // close file immediately
			return closeErr
		}
		if cpErr != nil {
			return cpErr
		}
		if n != fileInfo.Size() {
			return fmt.Errorf("unexpected bytes written: wrote %d, want %d", n, fileInfo.Size())
		}
		return err
	})

	return err
}

// Execute executes the root command.
func Execute() error {
	return rootCmd.Execute()
}

func er(msg interface{}) {
	fmt.Println("Error:", msg)
	os.Exit(1)
}

func evaluateFlagPair(cmd *cobra.Command, flag string) (bool, error) {
	posFlag := cmd.Flag(flag)
	negFlag := cmd.Flag("no-" + flag)
	if posFlag.Changed && negFlag.Changed {
		return false, errors.New("flags are mutually exclusive")
	}
	negVal, err := strconv.ParseBool(negFlag.Value.String())
	if err != nil {
		return false, err
	}
	if negVal {
		return false, nil
	}
	return cmd.Flags().GetBool(flag)
}

func init() {
	rootCmd.PersistentFlags().Bool("mamba", true, "Search for mamba")
	rootCmd.PersistentFlags().Bool("no-mamba", false, "")

	rootCmd.PersistentFlags().Bool("micromamba", true, "Search for micromamba, Can install")
	rootCmd.PersistentFlags().Bool("no-micromamba", false, "")

	rootCmd.PersistentFlags().Bool("conda", true, "Search for conda")
	rootCmd.PersistentFlags().Bool("no-conda", false, "")

	rootCmd.PersistentFlags().Bool("conda-exe", true, "Search for conda.exe/ conda standalong.  Can install")
	rootCmd.PersistentFlags().Bool("no-conda-exe", false, "")

	rootCmd.PersistentFlags().Bool("no-install", false, "Don't install stuff")

	// TODO: implement logger + verbosity
	rootCmd.PersistentFlags().IntP("verbosity", "v", 1, "verbosity level (0-3)")
}
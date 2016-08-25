package repobuilder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/job"
	"github.com/mongodb/amboy/registry"
	"github.com/mongodb/curator"
	"github.com/pkg/errors"
	"github.com/tychoish/grip"
)

// BuildRPMRepoJob contains specific implementation for building RPM
// repositories using createrepo.
type BuildRPMRepoJob struct {
	*Job
}

func init() {
	registry.AddJobType("build-rpm-repo", func() amboy.Job {
		return &BuildRPMRepoJob{buildRepoJob()}
	})
}

// NewBuildRPMRepo returns a new populated BuildRPMRepoJob object.
func NewBuildRPMRepo(conf *RepositoryConfig, distro *RepositoryDefinition, version, arch, profile string, pkgs ...string) (*BuildRPMRepoJob, error) {
	var err error
	r := &BuildRPMRepoJob{buildRepoJob()}

	r.WorkSpace, err = os.Getwd()
	if err != nil {
		grip.Errorln("system error: cannot determine the current working directory.",
			"not creating a job object.")
		return nil, err
	}

	r.release, err = curator.NewMongoDBVersion(version)
	if err != nil {
		return nil, err
	}

	r.JobType = amboy.JobType{
		Name:    "build-rpm-repo",
		Version: 0,
	}
	r.Name = fmt.Sprintf("build-rpm-repo.%d", job.GetNumber())
	r.Distro = distro
	r.Conf = conf
	r.PackagePaths = pkgs
	r.Version = version
	r.Arch = arch
	r.Profile = profile

	return r, nil
}

func (j *BuildRPMRepoJob) injectPackage(local, repoName string) (string, error) {
	repoPath := filepath.Join(local, repoName, j.Arch)
	err := j.linkPackages(filepath.Join(repoPath, "RPMS"))

	return repoPath, errors.Wrapf(err, "linking packages for %s", repoPath)
}

func (j *BuildRPMRepoJob) rebuildRepo(workingDir string, wg *sync.WaitGroup) {
	defer wg.Done()

	var output string
	var err error
	var out []byte

	cmd := exec.Command("createrepo", "-d", "-s", "sha", workingDir)
	grip.Infoln("building repo with operation:", strings.Join(cmd.Args, " "))

	if j.DryRun {
		output = "no output: dry run"
		grip.Noticeln("[dry-run] would run:", strings.Join(cmd.Args, " "))
	} else {
		grip.Noticeln("building repo with operation:", strings.Join(cmd.Args, " "))
		out, err = cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			j.addError(errors.Wrapf(err, "running createrepo for %s", workingDir))
			grip.Error(err)
			grip.Info(output)
			return
		}
		grip.Debug(output)
	}

	grip.Infoln("rebuilt repo for:", workingDir)
	j.mutex.Lock()
	j.Output[workingDir] = output
	j.mutex.Unlock()

	metaDataFile := filepath.Join(workingDir, "repodata", "repomd.xml")
	err = j.signFile(metaDataFile, "asc", false) // (name, extension, overwrite)
	if err != nil {
		j.addError(errors.Wrapf(err, "signing release metadata for %s", workingDir))
		return
	}

	err = j.Conf.BuildIndexPageForDirectory(workingDir, j.Distro.Bucket)
	if err != nil {
		j.addError(errors.Wrapf(err, "building index.html pages for %s", workingDir))
		return
	}
}

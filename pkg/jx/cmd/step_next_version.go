package cmd

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"encoding/json"

	"github.com/blang/semver"
	version "github.com/hashicorp/go-version"
	"github.com/jenkins-x/jx/pkg/gits"
	"github.com/jenkins-x/jx/pkg/jx/cmd/templates"
	cmdutil "github.com/jenkins-x/jx/pkg/jx/cmd/util"
	"github.com/jenkins-x/jx/pkg/log"
	"github.com/spf13/cobra"
)

const (
	packagejson = "package.json"
	chartyaml   = "Chart.yaml"
	pomxml      = "pom.xml"
	makefile    = "Makefile"
)

// StepNextVersionOptions contains the command line flags
type StepNextVersionOptions struct {
	Filename      string
	Dir           string
	Tag           bool
	UseGitTagOnly bool
	NewVersion    string
	StepOptions
}

type Project struct {
	Version string `xml:"version"`
}

type PackageJSON struct {
	Version string `json:"version"`
}

var (
	StepNextVersionLong = templates.LongDesc(`
		This pipeline step command works out a semantic version, writes a file ./VERSION and optionally updates a file
`)

	StepNextVersionExample = templates.Examples(`
		jx step next-version
		jx step next-version --filename package.json
		jx step next-version --filename package.json --tag
		jx step next-version --filename package.json --tag --version 1.2.3
`)
)

func NewCmdStepNextVersion(f cmdutil.Factory, out io.Writer, errOut io.Writer) *cobra.Command {
	options := StepNextVersionOptions{}
	cmd := &cobra.Command{
		Use:     "next-version",
		Short:   "Writes next semantic version",
		Long:    StepNextVersionLong,
		Example: StepNextVersionExample,
		Run: func(cmd *cobra.Command, args []string) {
			options.Cmd = cmd
			options.Args = args
			err := options.Run()
			cmdutil.CheckErr(err)
		},
	}
	cmd.Flags().StringVarP(&options.Filename, "filename", "f", "", "Filename that contains version property to update, e.g. package.json")
	cmd.Flags().StringVarP(&options.NewVersion, "version", "", "", "optional version to use rather than generating a new one")
	cmd.Flags().StringVarP(&options.Dir, "dir", "d", "", "the directory to look for files that contain a pom.xml or Makefile with the project version to bump")
	cmd.Flags().BoolVarP(&options.Tag, "tag", "t", false, "tag and push new version")
	cmd.Flags().BoolVarP(&options.UseGitTagOnly, "use-git-tag-only", "", false, "only use a git tag so work out new semantic version, else specify filename [pom.xml,package.json,Makefile,Chart.yaml]")

	options.addCommonFlags(cmd)
	return cmd
}

func (o *StepNextVersionOptions) Run() error {

	var err error
	if o.NewVersion == "" {
		o.NewVersion, err = o.getNewVersionFromTag()
		if err != nil {
			return err
		}
	}

	// in declaritive pipelines we sometimes need to write the version to a file rather than pass state
	err = ioutil.WriteFile("VERSION", []byte(o.NewVersion), 0755)
	if err != nil {
		return err
	}

	// if filename flag set and recognised then update version, commit
	if o.Filename != "" {
		err = o.setVersion()
		if err != nil {
			return err
		}
	}

	// if tag set then tag it
	if o.Tag {
		tagOptions := StepTagOptions{
			Flags: StepTagFlags{
				Version: o.NewVersion,
			},
			StepOptions: o.StepOptions,
		}
		err = tagOptions.Run()
		if err != nil {
			return err
		}
	}
	return nil
}

// gets the version from a source file
func (o *StepNextVersionOptions) getVersion() (string, error) {
	if o.UseGitTagOnly {
		return "", nil
	}
	if o.Filename == "" {
		// try and work out
		return "", fmt.Errorf("no filename flag set to work out next semantic version.  choose pom.xml, Chart.yaml, package.json, Makefile or set the flag use-git-tag-only")
	}

	switch o.Filename {
	case chartyaml:
		chartFile := filepath.Join(o.Dir, chartyaml)
		chart, err := ioutil.ReadFile(chartFile)
		if err != nil {
			return "", err
		}

		if o.Verbose {
			log.Infof("Found Chart.yaml\n")
		}
		scanner := bufio.NewScanner(strings.NewReader(string(chart)))
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "version") {
				parts := strings.Split(scanner.Text(), ":")

				v := strings.TrimSpace(parts[1])
				if v != "" {
					if o.Verbose {
						log.Infof("existing Chart version %v\n", v)
					}
					return v, nil
				}
			}
		}
	case packagejson:
		packageFile := filepath.Join(o.Dir, packagejson)
		p, err := ioutil.ReadFile(packageFile)
		if err != nil {
			return "", err
		}

		if o.Verbose {
			log.Infof("found %s\n", packagejson)
		}
		var jsPackage PackageJSON
		json.Unmarshal(p, &jsPackage)

		if jsPackage.Version != "" {
			if o.Verbose {
				log.Infof("existing version %s\n", jsPackage.Version)
			}
			return jsPackage.Version, nil
		}

	case pomxml:
		pomFile := filepath.Join(o.Dir, pomxml)
		p, err := ioutil.ReadFile(pomFile)
		if err != nil {
			return "", err
		}

		if o.Verbose {
			log.Infof("found pom.xml\n")
		}
		var project Project
		xml.Unmarshal(p, &project)
		if project.Version != "" {
			if o.Verbose {
				log.Infof("existing version %s\n", project.Version)
			}
			return project.Version, nil
		}

	case makefile:
		makefile := filepath.Join(o.Dir, makefile)
		m, err := ioutil.ReadFile(makefile)
		if err != nil {
			return "", err
		}

		if o.Verbose {
			log.Infof("found Makefile\n")
		}
		scanner := bufio.NewScanner(strings.NewReader(string(m)))
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "VERSION") || strings.HasPrefix(scanner.Text(), "VERSION ") || strings.HasPrefix(scanner.Text(), "VERSION:") || strings.HasPrefix(scanner.Text(), "VERSION=") {
				parts := strings.Split(scanner.Text(), "=")

				v := strings.TrimSpace(parts[1])
				if v != "" {
					if o.Verbose {
						log.Infof("existing Makefile version %s\n", v)
					}
					return v, nil
				}
			}
		}
	default:
		return "", fmt.Errorf("no recognised file to obtain current version from")
	}

	return "", fmt.Errorf("cannot find version for file %s\n", o.Filename)
}

func (o *StepNextVersionOptions) getLatestTag() (string, error) {
	// if repo isn't provided by flags fall back to using current repo if run from a git project
	var versionsRaw []string

	err := o.runCommand("git", "fetch", "--tags", "-v")
	if err != nil {
		return "", fmt.Errorf("error fetching tags: %v", err)
	}
	out, err := o.getCommandOutput("", "git", "tag")
	if err != nil {
		return "", err
	}
	str := strings.TrimSuffix(string(out), "\n")
	tags := strings.Split(str, "\n")

	if len(tags) == 0 {
		// if no current flags exist then lets start at 0.0.0
		return "0.0.0", fmt.Errorf("no existing tags found")
	}

	// build an array of all the tags
	versionsRaw = make([]string, len(tags))
	for i, tag := range tags {
		if o.Verbose {
			log.Infof("found tag %s\n", tag)
		}
		tag = strings.TrimPrefix(tag, "v")
		if tag != "" {
			versionsRaw[i] = tag
		}
	}

	// turn the array into a new collection of versions that we can sort
	var versions []*version.Version
	for _, raw := range versionsRaw {
		v, _ := version.NewVersion(raw)
		if v != nil {
			versions = append(versions, v)
		}
	}

	if len(versions) == 0 {
		// if no current flags exist then lets start at 0.0.0
		return "0.0.0", fmt.Errorf("no existing tags found")
	}

	// return the latest tag
	col := version.Collection(versions)
	if o.Verbose {
		log.Infof("version collection %v\n", col)
	}

	sort.Sort(col)
	latest := len(versions)
	if versions[latest-1] == nil {
		return "0.0.0", fmt.Errorf("no existing tags found")
	}
	return versions[latest-1].String(), nil
}

func (o *StepNextVersionOptions) getNewVersionFromTag() (string, error) {

	// get the latest github tag
	tag, err := o.getLatestTag()
	if err != nil && tag == "" {
		return "", err
	}

	sv, err := semver.Parse(tag)
	if err != nil {
		return "", err
	}

	majorVersion := sv.Major
	minorVersion := sv.Minor
	patchVersion := sv.Patch + 1

	// check if major or minor version has been changed
	baseVersion, err := o.getVersion()
	if err != nil {
		return "", err
	}

	// first use go-version to turn into a proper version, this handles 1.0-SNAPSHOT which semver doesn't
	baseMajorVersion := uint64(0)
	baseMinorVersion := uint64(0)
	basePatchVersion := uint64(0)

	if baseVersion != "" {
		tmpVersion, err := version.NewVersion(baseVersion)
		if err != nil {
			return "", err
		}
		bsv, err := semver.New(tmpVersion.String())
		if err != nil {
			return "", err
		}
		baseMajorVersion = bsv.Major
		baseMinorVersion = bsv.Minor
		basePatchVersion = bsv.Patch
	}

	if baseMajorVersion > majorVersion ||
		(baseMajorVersion == majorVersion &&
			(baseMinorVersion > minorVersion) || (baseMinorVersion == minorVersion && basePatchVersion > patchVersion)) {
		majorVersion = baseMajorVersion
		minorVersion = baseMinorVersion
		patchVersion = basePatchVersion
	}

	return fmt.Sprintf("%d.%d.%d", majorVersion, minorVersion, patchVersion), nil
}
func (o *StepNextVersionOptions) setVersion() error {
	var err error
	var matchField string
	var regex *regexp.Regexp
	filename := filepath.Join(o.Dir, o.Filename)
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}
	switch o.Filename {
	case packagejson:
		regex = regexp.MustCompile(`[0-9][0-9]{0,2}.[0-9][0-9]{0,2}(.[0-9][0-9]{0,2})?(.[0-9][0-9]{0,2})?(-development)?`)
		matchField = "\"version\": \""

	case chartyaml:
		regex = regexp.MustCompile(`[0-9][0-9]{0,2}.[0-9][0-9]{0,2}(.[0-9][0-9]{0,2})?(.[0-9][0-9]{0,2})?(-.*)?`)
		matchField = "version: "

	default:
		return fmt.Errorf("unrecognised filename %s, supported files are %s %s", o.Filename, packagejson, chartyaml)
	}

	lines := strings.Split(string(b), "\n")

	for i, line := range lines {
		if strings.Contains(line, matchField) {
			lines[i] = regex.ReplaceAllString(line, o.NewVersion)
		} else {
			lines[i] = line
		}
	}
	output := strings.Join(lines, "\n")
	err = ioutil.WriteFile(filename, []byte(output), 0644)
	if err != nil {
		return err
	}

	err = gits.GitAdd(o.Dir, o.Filename)
	if err != nil {
		return err
	}

	err = gits.GitCommitDir(o.Dir, fmt.Sprintf("Release %s", o.NewVersion))
	if err != nil {
		return err
	}
	return nil
}

func (o *StepNextVersionOptions) setPackageVersion(b []byte) error {
	jsPackage := PackageJSON{}
	err := json.Unmarshal(b, &jsPackage)
	if err != nil {
		return err
	}
	jsPackage.Version = o.NewVersion

	return nil
}

func (o *StepNextVersionOptions) setChartVersion(b []byte) error {
	return nil
}

func (o *StepNextVersionOptions) setPomVersion(b []byte) error {
	return nil
}

// returns a string array containing the git owner and repo name for a given URL
func getCurrentGitOwnerRepo(url string) []string {
	var OwnerNameRegexp = regexp.MustCompile(`([^:]+)(/[^\/].+)?$`)

	matched2 := OwnerNameRegexp.FindStringSubmatch(url)
	s := strings.TrimSuffix(matched2[0], ".git")

	return strings.Split(s, "/")
}

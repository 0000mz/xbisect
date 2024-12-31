package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	charmlog "github.com/charmbracelet/log"
	"github.com/pelletier/go-toml/v2"
)

const (
	kApplicationName       = "xbisect"
	kApplicationDescrption = "Utility for bisecting applications"
)

var (
	gLogFileHandler *os.File         = nil
	gLogger         *log.Logger      = nil
	gConsoleLogger  *charmlog.Logger = nil
	gConfig         Config
)

// The default appdata directory is $HOME/.xbisect.
// Specifying $XBISECT_HOME environment variable will override the
// default appdata directory.
func GetAppDataDir() string {
	var kDefaultAppdataDir string = path.Join(os.Getenv("HOME"), ".xbisect")
	var appdata_dir = os.Getenv("XBISECT_HOME")
	if len(appdata_dir) == 0 {
		appdata_dir = kDefaultAppdataDir
	}
	return appdata_dir
}

func SetupAppDataOrDie() {
	err := os.MkdirAll(path.Join(GetAppDataDir(), "repos"), os.ModePerm)
	if err != nil {
		gLogger.Fatal(err)
	}
}

func SetupLoggerOrDie(verbose bool) {
	logfile := path.Join(GetAppDataDir(), "log.txt")
	var err error
	gLogFileHandler, err = os.OpenFile(logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	var iowriters io.Writer
	if verbose {
		iowriters = io.MultiWriter(gLogFileHandler, os.Stdout)
	} else {
		iowriters = io.MultiWriter(gLogFileHandler)
	}
	gLogger = log.New(iowriters, "", log.Ldate|log.Ltime|log.Lshortfile)

	// console logger
	styles := charmlog.DefaultStyles()
	styles.Levels[charmlog.ErrorLevel] = lipgloss.NewStyle().
		SetString("ERROR").
		Padding(0, 1, 0, 1).
		Background(lipgloss.Color("204")).
		Foreground(lipgloss.Color("0"))
	styles.Keys["err"] = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
	styles.Values["err"] = lipgloss.NewStyle().Bold(true)
	styles.Levels[charmlog.InfoLevel] = lipgloss.NewStyle().
		SetString(">").
		Padding(0, 1, 0, 1).
		Foreground(lipgloss.Color("#38f2ae")).
		Bold(true)

	gConsoleLogger = charmlog.NewWithOptions(os.Stdout, charmlog.Options{
		ReportCaller:    false,
		ReportTimestamp: false,
	})
	gConsoleLogger.SetStyles(styles)
}

func CleanupLogger() {
	if gLogFileHandler != nil {
		gLogFileHandler.Close()
	}
}

func ConsoleLogInfo(format string, v ...any) {
	gConsoleLogger.Infof(format, v...)
	gLogger.Printf(format+"\n", v...)
}
func ConsoleLogError(format string, v ...any) {
	gConsoleLogger.Errorf(format, v...)
	gLogger.Printf(format+"\n", v...)
}

type Config interface {
	// Initializes the config file by creating it if it doesnt exist
	// and loading the data within the config file into memory.
	InitOrDie()

	HasRepo(reponame string) bool
	GetRepo(reponame string) *RepoInfo
	// Add the repo to the config if it does not already exist.
	AddRepo(reponame string, location string, remote string) bool

	Save()
}

type RepoInfo struct {
	Remote string
	// The location of the repo on the user's local filesystem
	LocalPath string
	Name      string
}

type ConfigLayout struct {
	Repos []RepoInfo
}

type ConfigImpl struct {
	data            *ConfigLayout
	config_filepath string
}

func (c *ConfigImpl) GetRepo(reponame string) *RepoInfo {
	if c.data == nil {
		return nil
	}
	reponame = strings.ToLower(reponame)
	for _, repo := range c.data.Repos {
		if repo.Name == reponame {
			return &repo
		}
	}
	return nil
}

func (c *ConfigImpl) AddRepo(reponame string, location string, remote string) bool {
	reponame = strings.ToLower(reponame)
	if c.HasRepo(reponame) {
		return false
	}
	c.data.Repos = append(c.data.Repos, RepoInfo{Remote: remote, LocalPath: location, Name: reponame})
	return true
}

func (c *ConfigImpl) HasRepo(reponame string) bool {
	return c.GetRepo(reponame) != nil
}

func (c *ConfigImpl) Save() {
	if c.data == nil {
		return
	}
	serialized, err := toml.Marshal(c.data)
	if err != nil {
		gLogger.Fatalf("Failed to serialize config: %v", err)
	}
	err = os.WriteFile(c.config_filepath, serialized, 0666)
	if err != nil {
		gLogger.Fatalf("Failed to write config to %s: %v", c.config_filepath, err)
	}
}

func (c *ConfigImpl) InitOrDie() {
	c.config_filepath = path.Join(GetAppDataDir(), "config.toml")
	// Ensure that the file exists.
	f, err := os.OpenFile(c.config_filepath, os.O_CREATE|os.O_RDONLY, 0666)
	if err != nil {
		gLogger.Fatal(err)
	}
	f.Close()

	data, err := os.ReadFile(c.config_filepath)
	if err != nil {
		gLogger.Fatal(err)
	}

	c.data = &ConfigLayout{}
	err = toml.Unmarshal(data, c.data)
	if err != nil {
		gLogger.Fatal(err)
	}
}

func InitConfigOrDie() {
	cfg := &ConfigImpl{}
	gConfig = cfg

	gConfig.InitOrDie()
}

func filepathExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return err == nil // !os.IsNotExist(err)
}

func ImportGitRepo(repo_url string, name string) bool {
	if len(name) == 0 {
		ConsoleLogError("--name not specified for repo import.")
		return false
	}
	if matched, err := regexp.MatchString("^[a-zA-Z0-9_-]+$", name); !matched || err != nil {
		ConsoleLogError("Invalid repo name. Only alphanumeric and underscore/dash allowed.")
		if err != nil {
			gLogger.Printf("Regex error: %v\n", err)
		}
		return false
	}
	name = strings.ToLower(name)
	if len(repo_url) == 0 {
		ConsoleLogError("--git is empty.")
		return false
	}
	if gConfig.HasRepo(name) {
		ConsoleLogError("Repo \"%s\" already exists.", name)
		return false
	}

	var err error
	clonedir := path.Join(GetAppDataDir(), "repos", name)
	gLogger.Printf("Removing directory before cloning new repo into it: [exists? %t] %s\n",
		filepathExists(clonedir), clonedir)
	if err = os.RemoveAll(clonedir); err != nil {
		gLogger.Printf("Error: %v", err)
		ConsoleLogError("System error")
		return false
	}

	ConsoleLogInfo("Cloning git repo: %s", repo_url)
	err = runCommand("git", "clone", repo_url, clonedir)
	if err != nil {
		gLogger.Printf("Error: %v\n", err)
		ConsoleLogError("Git clone failed")
		return false
	}
	gConfig.AddRepo(name, clonedir, repo_url)
	return true
}

func CleanCache() bool {
	cachedir := path.Join(GetAppDataDir(), "cache")
	err := os.RemoveAll(cachedir)
	if err != nil {
		gLogger.Printf("Error: %v\n", err)
		ConsoleLogError("Error occurred when removing cache dir")
		return false
	} else {
		ConsoleLogInfo("Successfully cleaned up cache.")
	}
	return true
}

func runCommand(command ...string) error {
	return runCommandDir("", command...)
}

func runCommandDir(dir string, command ...string) error {
	if len(command) < 1 {
		return fmt.Errorf("Empty command")
	}
	gLogger.Printf("Running command: %s\n", strings.Join(command, " "))
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdout = gLogFileHandler
	cmd.Stderr = gLogFileHandler
	if len(dir) > 0 {
		cmd.Dir = dir
	}
	return cmd.Run()

}

func RunBisect(reponame, lo, hi string) bool {
	repo := gConfig.GetRepo(reponame)
	if repo == nil {
		ConsoleLogError("No imported repo with name: \"%s\". Run %s import --help",
			reponame, kApplicationName)
		return false
	}

	cachedir := ""
	for {
		hint_dirname := fmt.Sprintf("%s_%d", reponame, rand.Int())
		cachedir = path.Join(GetAppDataDir(), "cache", hint_dirname)
		gLogger.Printf("Considering cache dir: %s\n", cachedir)
		if !filepathExists(cachedir) {
			break
		}
	}

	var err error
	err = os.MkdirAll(cachedir, os.ModePerm)
	if err != nil {
		gLogger.Printf("Error: %v\n", err)
		ConsoleLogError("Failed to create cache dir: %s", cachedir)
		return false
	}
	ConsoleLogInfo("Using cache directory for bisect: %s", cachedir)

	// Copy the repo source to the cache location.
	cacherepo := path.Join(cachedir, "_repo")
	{
		if err = runCommand("cp", "--recursive", repo.LocalPath, cacherepo); err != nil {
			gLogger.Printf("Error: %v\n", err)
			ConsoleLogError("Failed to copy repo to cache location.")
			return false
		}
	}

	ConsoleLogInfo("Lo: %s", lo)
	ConsoleLogInfo("Hi: %s", hi)

	// DBG: Create tempfile for the script that will be executed in the bisect
	// operation.
	tmpfile, err := os.CreateTemp("", "bisect_script")
	if err != nil {
		ConsoleLogError("Failed to create temp bisect script")
		return false
	}

	{
		script := `
		echo "Running bisect on current hash"
		echo "cwd: $(pwd)"
		go run . > /tmp/compute 2>&1
		cat /tmp/compute
		`
		if _, err = tmpfile.WriteString(script); err != nil {
			ConsoleLogError("Failed to write script data to tempfile")
			gLogger.Printf("Error: %v\n", err)
			return false
		}
		tmpfile.Close()
		runCommand("chmod", "+x", tmpfile.Name()) // Give exec perms
	}

	command_sequence := [][]string{
		// Ensure that no bisect is running. This will do nothing if
		// it is not in bisect mode.
		{"git", "bisect", "reset"},
		{"git", "bisect", "start"},
		// TODO: The good and bad are not always synonymous w/ lo and hi commit hash...
		{"git", "bisect", "good", lo},
		{"git", "bisect", "bad", hi},
	}

	for _, cmd := range command_sequence {
		if err = runCommandDir(cacherepo, cmd...); err != nil {
			gLogger.Printf("Error: %v\n", err)
			ConsoleLogError("Error setting up bisect state.")
			return false
		}
	}
	ConsoleLogInfo("Running bisect script")
	defer func() {
		gLogger.Println("Resetting git bisect")
		runCommandDir(cacherepo, "git", "bisect", "reset")
	}()
	{
		cmd := exec.Command("git", "bisect", "run", tmpfile.Name())
		cmd.Dir = cacherepo
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			gLogger.Printf("Error: %v\n", err)
			ConsoleLogError("Error occurred setting up git bisect output streaming.")
			return false
		}
		if err = cmd.Start(); err != nil {
			gLogger.Printf("Error: %v\n", err)
			ConsoleLogError("Failed to start git bisect")
			return false
		}

		// Use a teewriter to output to the logfile and also scan the
		// output.
		var buf bytes.Buffer
		tee := io.TeeReader(stdout, &buf)

		hashLineRe := regexp.MustCompile(`^\[(.*)\] .*$`)

		scanner := bufio.NewScanner(tee)
		var lines_until_hash int64 = 0

		var scan_succeed bool
		scan_succeed = true

		for scanner.Scan() {
			lines_until_hash -= 1

			line := scanner.Text()
			if matches, _ := regexp.MatchString("^Bisecting: [0-9]+ revision(s)? left to test after this \\(roughly [0-9]+ step(s)?\\)$", line); matches {
				lines_until_hash = 1
			} else if lines_until_hash == 0 {
				hashes := hashLineRe.FindStringSubmatch(line)
				if len(hashes) != 2 {
					ConsoleLogError("Failed to parse log of git message")
					scan_succeed = false
					break
				}
				hash := hashes[1]
				ConsoleLogInfo("Hash %s", hash)
			}
		}
		buf.WriteTo(gLogger.Writer())
		if !scan_succeed {
			return false
		}

		if err = cmd.Wait(); err != nil {
			gLogger.Printf("Error: %v\n", err)
			ConsoleLogError("Failed to run git bisect")
			return false
		}
	}
	return true
}

var cli struct {
	Verbose bool `cmd:"" help:"Log everything to console." default:"false"`

	Run struct {
		Repo string `help:"Run bisect operation for the given project." short:"r"`
		Lo   string `help:"Hash of the earlier commit."`
		Hi   string `help:"Hash of the later commit."`
	} `cmd:"" help:"Run a bisect operation"`

	Import struct {
		Git  string `help:"Import repo from remote git url"`
		Name string `help:"The name to reference the repo by"`
	} `cmd:"" help:"Import remote projects that you want to run bisect on."`

	Clean struct {
	} `cmd:"" help:"Clean up the cache."`
}

func Main() int {
	ctx := kong.Parse(&cli,
		kong.Name(kApplicationName),
		kong.Description(kApplicationDescrption),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
			Summary: true,
		}))

	SetupLoggerOrDie(cli.Verbose)

	SetupAppDataOrDie()
	InitConfigOrDie()
	// Cleanups
	defer func() {
		CleanupLogger()
		gConfig.Save()
	}()

	var success bool = false
	switch ctx.Command() {
	case "import":
		success = ImportGitRepo(cli.Import.Git, cli.Import.Name)
	case "run":
		success = RunBisect(cli.Run.Repo, cli.Run.Lo, cli.Run.Hi)
	case "clean":
		success = CleanCache()
	}
	if !success {
		return 1
	}
	return 0
}

func main() {
	os.Exit(Main())
}

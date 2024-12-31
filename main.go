package main

import (
	"io"
	"log"
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
	// Add the repo to the config if it does not already exist.
	AddRepo(reponame string, location string, remote string) bool
	Save()
}

type ConfigLayout struct {
	Repos []struct {
		Remote string
		// The location of the repo on the user's local filesystem
		LocalPath string
		Name      string
	}
}

type ConfigImpl struct {
	data            *ConfigLayout
	config_filepath string
}

func (c *ConfigImpl) AddRepo(reponame string, location string, remote string) bool {
	if c.data == nil {
		return false
	}
	if c.HasRepo(reponame) {
		return false
	}
	c.data.Repos = append(c.data.Repos, struct {
		Remote    string
		LocalPath string
		Name      string
	}{Remote: remote, LocalPath: location, Name: reponame})
	return true
}

func (c *ConfigImpl) HasRepo(reponame string) bool {
	if c.data == nil {
		return false
	}
	for _, repo := range c.data.Repos {
		if repo.Name == reponame {
			return true
		}
	}
	return false
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
	return err != nil // !os.IsNotExist(err)
}

func ImportGitRepo(repo_url string, name string) {
	if len(name) == 0 {
		ConsoleLogError("--name not specified for repo import.")
		os.Exit(1)
	}
	if matched, err := regexp.MatchString("^[a-zA-Z0-9_-]+$", name); !matched || err != nil {
		ConsoleLogError("Invalid repo name. Only alphanumeric and underscore/dash allowed.")
		if err != nil {
			gLogger.Printf("Regex error: %v\n", err)
		}
		os.Exit(1)
	}
	name = strings.ToLower(name)
	if len(repo_url) == 0 {
		ConsoleLogError("--git is empty.")
		os.Exit(1)
	}
	if gConfig.HasRepo(name) {
		ConsoleLogError("Repo \"%s\" already exists.", name)
		os.Exit(1)
	}

	var err error
	clonedir := path.Join(GetAppDataDir(), "repos", name)
	gLogger.Printf("Removing directory before cloning new repo into it: [exists? %t] %s\n",
		filepathExists(clonedir), clonedir)
	if err = os.RemoveAll(clonedir); err != nil {
		gLogger.Printf("Error: %v", err)
		ConsoleLogError("System error")
		os.Exit(1)
	}

	command_args := []string{"git", "clone", repo_url, clonedir}
	gLogger.Printf("Running: %s\n", strings.Join(command_args, " "))
	cmd := exec.Command(command_args[0], command_args[1:]...)
	cmd.Stdout = gLogFileHandler
	cmd.Stderr = gLogFileHandler

	ConsoleLogInfo("Cloning git repo: %s", repo_url)
	err = cmd.Start()
	if err != nil {
		gLogger.Printf("Error: %v\n", err)
		ConsoleLogError("Git clone failed")
		os.Exit(1)
	}
	err = cmd.Wait()
	if err != nil {
		gLogger.Printf("Error: %v\n", err)
		ConsoleLogError("Git clone failed")
		os.Exit(1)
	}
	gConfig.AddRepo(name, clonedir, repo_url)
}

var cli struct {
	Verbose bool `cmd:"" help:"Log everything to console" default:"false"`

	Run struct {
		Repo string `help:"Run bisect operation for the given project" short:"r"`
	} `cmd:"" help:"Run a bisect operation"`

	Import struct {
		Git  string `help:"Import repo from remote git url"`
		Name string `help:"The name to reference the repo by"`
	} `cmd:"" help:"Import remote projects that you want to run bisect on."`
}

func main() {
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

	switch ctx.Command() {
	case "import":
		ImportGitRepo(cli.Import.Git, cli.Import.Name)
	}
}

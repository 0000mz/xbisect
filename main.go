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
	"strconv"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	charmlog "github.com/charmbracelet/log"
	"github.com/pelletier/go-toml/v2"
)

const (
	kApplicationName       = "xbisect"
	kApplicationDescrption = "Utility for bisecting applications"
	// TODO: Make this a pre-compiled regex
	kAlphanumericDashUnderlineRe = "^[a-zA-Z0-9_-]+$"

	// Color Codes
	kColorRed     = "\033[31m"
	kColorGreen   = "\033[32m"
	kColorCyan    = "\033[36m"
	kColorGray    = "\033[37m"
	kFontBold     = "\033[1m"
	kConsoleReset = "\033[0m"

	kBisectSkipCode = 125
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
	if matched, err := regexp.MatchString(kAlphanumericDashUnderlineRe, name); !matched || err != nil {
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

func runCommandDirOutput(dir string, command ...string) ([]byte, error) {
	if len(command) < 1 {
		return nil, fmt.Errorf("Empty command")
	}
	gLogger.Printf("Running command: %s\n", strings.Join(command, " "))
	cmd := exec.Command(command[0], command[1:]...)
	if len(dir) > 0 {
		cmd.Dir = dir
	}
	return cmd.Output()
}

func RunBisect(reponame, lo, hi string, steps []string) bool {
	repo := gConfig.GetRepo(reponame)
	if repo == nil {
		ConsoleLogError("No imported repo with name: \"%s\". Run %s import --help",
			reponame, kApplicationName)
		return false
	}
	if len(steps) == 0 {
		ConsoleLogError("No steps provided to execute.")
		return false
	}
	for _, step := range steps {
		if matched, err := regexp.MatchString(kAlphanumericDashUnderlineRe, step); !matched || err != nil {
			ConsoleLogError("Invalid step name. Only alphanumeric and underscore/dash allowed.")
			if err != nil {
				gLogger.Printf("Regex error: %v\n", err)
			}
			return false
		}
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
		# test $(cat /tmp/compute | awk '$2 < 40 { print }' | wc -l) -gt 0 || exit 125
		test $(cat /tmp/compute | awk '$2 < 40 { print }' | wc -l) -gt 0
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

	initial_commit_hash_b, err := runCommandDirOutput(cacherepo, "git", "rev-parse", "HEAD")
	if err != nil || len(initial_commit_hash_b) == 0 {
		if err != nil {
			gLogger.Printf("Error: %v", err)
		}
		ConsoleLogError("Failed to get current commit hash")
		return false
	}
	initial_commit_hash := strings.TrimSpace(string(initial_commit_hash_b))
	gLogger.Printf("Repo initial commit hash: %s\n", initial_commit_hash)
	ConsoleLogInfo("Running bisect script")
	defer func() {
		gLogger.Println("Resetting git bisect")
		runCommandDir(cacherepo, "git", "bisect", "reset")
	}()
	{
		_wrap_step := func(script_path, step string) string {
			return fmt.Sprintf(`
				STEP_NAME=%s
				%s "${STEP_NAME}"
				RESULT=$?
				if [ $RESULT -eq 0 ]
				then
					echo "xbisect step=${STEP_NAME} PASS"
				else
					echo "xbisect step=${STEP_NAME} FAIL res=${RESULT}"
					exit $RESULT
				fi
			`, step, script_path)
		}

		// Create a script that will run the main script for each step provided
		// by the caller.
		script_file := tmpfile.Name()
		wrapper_script_file, err := os.CreateTemp("", "bisect_script_wrapper")
		wrapper_script := ``
		for _, step := range steps {
			wrapper_script += _wrap_step(script_file, step) // fmt.Sprintf("%s %s\n", script_file, step)
		}
		gLogger.Printf("Wrapper Script:\n%s\n", wrapper_script)
		if _, err = wrapper_script_file.WriteString(wrapper_script); err != nil {
			gLogger.Printf("Error: %v\n", err)
			ConsoleLogError("Failed to create wrapper script")
			wrapper_script_file.Close()
			return false
		}
		wrapper_script_file.Close()
		runCommand("chmod", "+x", wrapper_script_file.Name()) // Give exec perms

		cmd := exec.Command("git", "bisect", "run", wrapper_script_file.Name())
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
		statusMatchRe := regexp.MustCompile(`xbisect step=([a-zA-Z0-9_-]+) (PASS|FAIL)( res=[0-9]+)?`)
		resMatchRe := regexp.MustCompile(`res=([0-9]+)`)

		scanner := bufio.NewScanner(tee)
		var lines_until_hash int64 = 0

		var scan_succeed bool
		scan_succeed = true

		type StepResult struct {
			Name       string
			Pass       bool
			ExitStatus int
		}
		type CommitResult struct {
			Hash        string
			StepResults []StepResult
		}
		_get_exit_status := func(data string) (int, error) {
			if len(data) == 0 {
				return 0, nil
			}
			res_match := resMatchRe.FindStringSubmatch(data)
			if res_match == nil {
				return 0, fmt.Errorf("Regex found no match")
			}
			exit_code, err := strconv.Atoi(res_match[1])
			if err != nil {
				return 0, err
			}
			return exit_code, nil
		}

		var commit_results map[string]*CommitResult = make(map[string]*CommitResult)
		var current_result *CommitResult = nil
		nb_commit_parse_from_current_line := 0
		nb_commit_parse_from_regex := 0

		for scanner.Scan() {
			lines_until_hash -= 1

			line := strings.TrimSpace(scanner.Text())
			// TODO: Use pre-compiled regex for all of these cases
			if matches, _ := regexp.MatchString("^Bisecting: [0-9]+ revision(s)? left to test after this \\(roughly [0-9]+ step(s)?\\)$", line); matches {
				lines_until_hash = 1
			} else if xbisect_status_match := statusMatchRe.FindStringSubmatch(line); xbisect_status_match != nil {
				gLogger.Printf("xbisect_status_match: len=%d\n", len(xbisect_status_match))

				res := StepResult{}
				res.Name = xbisect_status_match[1]
				res.Pass = xbisect_status_match[2] == "PASS"
				res.ExitStatus, err = _get_exit_status(xbisect_status_match[3])
				if err != nil {
					gLogger.Printf("Error: %v\n", err)
					ConsoleLogError("Failed to parse status of bisect step")
					scan_succeed = false
					break
				}
				if current_result == nil {
					ConsoleLogError("Found bisect result before hash")
					scan_succeed = false
					break
				}
				current_result.StepResults = append(current_result.StepResults, res)
			}

			current_hash_from_line := ""
			if lines_until_hash == 0 {
				hashes := hashLineRe.FindStringSubmatch(line)
				if len(hashes) != 2 {
					ConsoleLogError("Failed to parse log of git message")
					scan_succeed = false
					break
				}
				nb_commit_parse_from_regex += 1
				current_hash_from_line = hashes[1]
			} else if line == "Running bisect on current hash" {
				// NOTE: The log: "Running bisect on current hash" is always logged. If it is the
				// first log, there is not going to be a preceding line that informs what the
				// current has his. In this case, the starting hash is pre-parsed, and once
				// this log is found the very first time, it uses the pre-parsed initial commit
				// hash.
				if nb_commit_parse_from_regex == 0 && nb_commit_parse_from_current_line == 0 {
					current_hash_from_line = initial_commit_hash
				}
				nb_commit_parse_from_current_line += 1
			}

			if len(current_hash_from_line) > 0 {
				if _, has_hash := commit_results[current_hash_from_line]; has_hash {
					ConsoleLogError("Detected duplicate commit: %s", current_hash_from_line)
					scan_succeed = false
					break
				}
				current_result = &CommitResult{}
				current_result.Hash = current_hash_from_line
				commit_results[current_hash_from_line] = current_result
			}
		}
		gLogger.Printf("BISECT STREAM DUMP START>>>\n")
		buf.WriteTo(gLogger.Writer())
		gLogger.Printf("BISECT STREAM DUMP END>>>\n")
		if !scan_succeed {
			return false
		}

		for hash, result := range commit_results {
			for _, step := range result.StepResults {
				success_log := func() string {
					if step.Pass {
						return fmt.Sprintf("%s%sPASS%s", kFontBold, kColorGreen, kConsoleReset)
					} else if step.ExitStatus == kBisectSkipCode {
						return fmt.Sprintf("%s%sSKIP%s", kFontBold, kColorGray, kConsoleReset)
					} else {
						return fmt.Sprintf("%s%sFAIL%s", kFontBold, kColorRed, kConsoleReset)
					}
				}()
				step_log := fmt.Sprintf("%s%s%s", kColorCyan, step.Name, kConsoleReset)
				ConsoleLogInfo("%s %s %s", hash, step_log, success_log)
			}
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
		Repo  string   `help:"Run bisect operation for the given project." short:"r"`
		Lo    string   `help:"Hash of the earlier commit."`
		Hi    string   `help:"Hash of the later commit."`
		Steps []string `help:"List of steps in the  bisect script. Each step will be passed to the bisect script as first argument and will record the return value each step as the status of the bisect."`
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
		success = RunBisect(cli.Run.Repo, cli.Run.Lo, cli.Run.Hi, cli.Run.Steps)
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

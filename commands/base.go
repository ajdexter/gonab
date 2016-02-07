package commands

import (
	"github.com/Sirupsen/logrus"
	"github.com/hobeone/gonab/config"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	// App is the main hook to run
	App        = kingpin.New("gonab", "A usenet indexer")
	debug      = App.Flag("debug", "Enable Debug mode.").Bool()
	debugdb    = App.Flag("debugdb", "Log Database queries (noisy).").Default("false").Bool()
	configfile = App.Flag("config", "Config file to use").Default("config.json").ExistingFile()
)

func SetupCommands() {
	rcmd := &ReleasesCommand{}
	rcmd.configure(App)

	scanner := &ScanCommand{}
	scanner.configure(App)

	App.Command("createdb", "Create Database and Tables.").Action(createdb)

	bcmd := &BinariesCommand{}
	App.Command("makebinaries", "Create binaries from parts").Action(bcmd.run)

	regexcmd := &RegexImporter{}
	App.Command("importregex", "Import regexes from nzedb").Action(regexcmd.run)
}

func loadConfig(cfile string) *config.Config {
	if len(cfile) == 0 {
		logrus.Infof("No --config_file given.  Using default: %s", *configfile)
		cfile = *configfile
	}

	logrus.Infof("Got config file: %s\n", cfile)
	cfg := config.NewConfig()
	err := cfg.ReadConfig(cfile)
	if err != nil {
		logrus.Fatal(err)
	}

	// Override cfg from flags
	cfg.DB.Verbose = *debugdb
	return cfg
}

package db

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/OneOfOne/xxhash"
	"github.com/Sirupsen/logrus"
	"github.com/davecgh/go-spew/spew"
	"github.com/hobeone/gonab/nzb"
	"github.com/hobeone/gonab/types"
	"github.com/jinzhu/gorm"

	// Import sqlite
	_ "github.com/mattn/go-sqlite3"
)

//Handle Struct
type Handle struct {
	DB           gorm.DB
	writeUpdates bool
	syncMutex    sync.Mutex
}

// debugLogger satisfies Gorm's logger interface
// so that we can log SQL queries at Logrus' debug level
type debugLogger struct{}

func (*debugLogger) Print(msg ...interface{}) {
	logrus.Debug(msg)
}

func openDB(dbType string, dbArgs string, verbose bool) gorm.DB {
	logrus.Infof("Opening database %s:%s", dbType, dbArgs)
	// Error only returns from this if it is an unknown driver.
	d, err := gorm.Open(dbType, dbArgs)
	if err != nil {
		panic(err.Error())
	}
	d.SingularTable(true)
	d.SetLogger(&debugLogger{})
	d.LogMode(verbose)
	// Actually test that we have a working connection
	err = d.DB().Ping()
	if err != nil {
		panic(err.Error())
	}
	return d
}

func setupDB(db gorm.DB) error {
	tx := db.Begin()
	err := tx.AutoMigrate(&types.Group{}, &types.Release{}, &types.Binary{}, &types.Part{}, &types.Segment{}, &types.Regex{}).Error
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	err = db.Exec("PRAGMA journal_mode=WAL;").Error
	if err != nil {
		return err
	}
	err = db.Exec("PRAGMA synchronous = NORMAL;").Error
	if err != nil {
		return err
	}
	err = db.Exec("PRAGMA encoding = \"UTF-8\";").Error
	if err != nil {
		return err
	}

	return nil
}

func constructDBPath(dbpath string, memory bool) string {
	mode := "rwc"
	if memory {
		mode = "memory"
	}
	return fmt.Sprintf("file:%s?cache=shared&mode=%s", dbpath, mode)
}

// CreateAndMigrateDB will create a new database on disk and create all tables.
func CreateAndMigrateDB(dbpath string, verbose bool) (*Handle, error) {
	constructedPath := constructDBPath(dbpath, false)
	db := openDB("sqlite3", constructedPath, verbose)
	err := setupDB(db)
	if err != nil {
		return nil, err
	}
	return &Handle{DB: db}, nil
}

// NewDBHandle creates a new DBHandle
//	dbpath: the path to the database to use.
//	verbose: when true database accesses are logged to stdout
func NewDBHandle(dbpath string, verbose bool) *Handle {
	constructedPath := constructDBPath(dbpath, false)
	db := openDB("sqlite3", constructedPath, verbose)
	return &Handle{DB: db}
}

// NewMemoryDBHandle creates a new in memory database.  Only used for testing.
// The name of the database is a random string so multiple tests can run in
// parallel with their own database.  This will setup the database with the
// all the tables as well.
func NewMemoryDBHandle(verbose bool) *Handle {
	dbpath := randString()
	constructedPath := constructDBPath(dbpath, true)
	db := openDB("sqlite3", constructedPath, verbose)
	err := setupDB(db)
	if err != nil {
		panic(err.Error())
	}
	return &Handle{DB: db}
}

func randString() string {
	rb := make([]byte, 32)
	_, err := rand.Read(rb)
	if err != nil {
		fmt.Println(err)
	}
	return base64.URLEncoding.EncodeToString(rb)
}

// CreatePart func
func (d *Handle) CreatePart(p *types.Part) error {
	return d.DB.Save(p).Error
}

// ListParts func
func (d *Handle) ListParts() {
	var parts []types.Part
	err := d.DB.Preload("Segments").Find(&parts).Error
	if err != nil {
		fmt.Printf("Error getting parts: %v\n", err)
	}

	for _, p := range parts {
		fmt.Printf("Part: %s\n", p.Subject)
		fmt.Printf("  Segments: %d\n", p.TotalSegments)
		fmt.Printf("  Available Segments: %d\n", len(p.Segments))
	}
}

// FindGroupByName does what it says
func (d *Handle) FindGroupByName(name string) (*types.Group, error) {
	var g types.Group
	err := d.DB.Where("name = ?", name).First(&g).Error
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// GetActiveGroups returns all groups marked active in the db
func (d *Handle) GetActiveGroups() ([]types.Group, error) {
	var g []types.Group
	err := d.DB.Where("active = ?", true).Find(&g).Error
	if err != nil {
		return nil, err
	}
	return g, nil
}

// PartRegex Comment
var PartRegex = regexp.MustCompile(`(?i)[\[\( ]((\d{1,3}\/\d{1,3})|(\d{1,3} of \d{1,3})|(\d{1,3}-\d{1,3})|(\d{1,3}~\d{1,3}))[\)\] ]`)

func hasNameAndParts(m map[string]string) bool {
	_, nameok := m["name"]
	_, partok := m["parts"]
	return nameok && partok
}

func makeBinaryHash(name, group, from, totalParts string) string {
	h := xxhash.New64()
	h.Write([]byte(name + group + from + totalParts))
	return fmt.Sprintf("%x", h.Sum64())
}

// MakeBinaries comment
func (d *Handle) MakeBinaries() error {
	r := `(?i).*?(?P<parts>\d{1,3}\/\d{1,3}).*?\"(?P<name>.*?)\.(sample|mkv|Avi|mp4|vol|ogm|par|rar|sfv|nfo|nzb|srt|ass|mpg|txt|zip|wmv|ssa|r\d{1,3}|7z|tar|mov|divx|m2ts|rmvb|iso|dmg|sub|idx|rm|ac3|t\d{1,2}|u\d{1,3})`
	rc := types.RegexpUtil{regexp.MustCompile(r)}
	var parts []types.Part
	err := d.DB.Where("binary_id is NULL").Find(&parts).Error
	if err != nil {
		return err
	}

	binaries := map[string]*types.Binary{}

	for _, p := range parts {

		m := rc.FindStringSubmatchMap(p.Subject)
		if len(m) > 0 {
			for k, v := range m {
				m[k] = strings.TrimSpace(v)
			}
		}
		// fill name if reqid is available
		if reqid, ok := m["reqid"]; ok {
			if _, okname := m["name"]; !okname {
				m["name"] = reqid
			}
		}

		// Generate a name if we don't have one
		if _, ok := m["name"]; !ok {
			var matchvalues []string
			for _, v := range m {
				matchvalues = append(matchvalues, v)
			}
			m["name"] = strings.Join(matchvalues, " ")
		}

		// Look for parts manually if the regex didn't return some
		if _, ok := m["parts"]; !ok {
			partmatch := PartRegex.FindStringSubmatch(p.Subject)
			if partmatch != nil {
				m["parts"] = partmatch[1]
			}
		}
		if !hasNameAndParts(m) {
			fmt.Printf("Couldn't find Name and Parts for %s\n", p.Subject)
			spew.Dump(m)
			continue
		}

		// Clean name of '-', '~', ' of '
		if strings.Index(m["parts"], "/") == -1 {
			m["parts"] = strings.Replace(m["parts"], "-", "/", -1)
			m["parts"] = strings.Replace(m["parts"], "~", "/", -1)
			m["parts"] = strings.Replace(m["parts"], " of ", "/", -1)
			m["parts"] = strings.Replace(m["parts"], "[", "", -1)
			m["parts"] = strings.Replace(m["parts"], "]", "", -1)
			m["parts"] = strings.Replace(m["parts"], "(", "", -1)
			m["parts"] = strings.Replace(m["parts"], ")", "", -1)
		}

		if strings.Index(m["parts"], "/") == -1 {
			fmt.Printf("Couldn't find valid parts information for %s (%s didn't include /)\n", p.Subject, m["parts"])
			continue
		}

		partcounts := strings.SplitN(m["parts"], "/", 2)

		binhash := makeBinaryHash(m["name"], p.Group, p.From, partcounts[1])
		if bin, ok := binaries[binhash]; ok {
			bin.Parts = append(bin.Parts, p)
		} else {
			totalparts, _ := strconv.Atoi(partcounts[1])
			binaries[binhash] = &types.Binary{
				Hash:       binhash,
				Name:       m["name"],
				Posted:     p.Posted,
				From:       p.From,
				Parts:      []types.Part{p},
				Group:      p.Group,
				TotalParts: totalparts,
			}
		}
		err = d.DB.Save(binaries[binhash]).Error
		if err != nil {
			return err
		}
	}
	return nil
}

var removeChars = []string{"#", "@", "$", "%", "^", "§", "¨", "©", "Ö"}
var spaceChars = []string{"_", ".", "-"}

// Strip bad chars out of name for API to match on
func cleanReleaseName(name string) string {
	for _, c := range removeChars {
		name = strings.Replace(name, c, "", -1)
	}
	for _, c := range spaceChars {
		name = strings.Replace(name, c, " ", -1)
	}
	return name
}

// MakeReleases comment
func (d *Handle) MakeReleases() error {
	var binaries []types.Binary
	q := `SELECT binary.id, binary.name, binary.posted, binary.total_parts, binary.'group'
	FROM binary
	INNER JOIN (
			SELECT
					part.id, part.binary_id, part.total_segments, count(*) as available_segments
			FROM part
					INNER JOIN segment ON part.id = segment.part_id
			GROUP BY part.id
			) as part
			ON binary.id = part.binary_id
	GROUP BY binary.id
	HAVING count(*) >= binary.total_parts AND (sum(part.available_segments) / sum(part.total_segments)) * 100 >= ?
	ORDER BY binary.posted DESC`
	err := d.DB.Raw(q, 100).Scan(&binaries).Error
	if err != nil {
		return err
	}
	for _, b := range binaries {
		// See if a Release already exists for this binary name/date
		dbrel := &types.Release{}
		err := d.DB.Where("name = ? and posted = ?", b.Name, b.Posted).First(&dbrel).Error
		if err != nil && err != gorm.RecordNotFound {
			return err
		}
		if dbrel.ID != 0 {
			logrus.Infof("Duplicate Binary found, deleting: %s", b.Name)
			//Delete here
			continue
		}

		dbbin := &types.Binary{}
		err = d.DB.Preload("Parts").Preload("Parts.Segments").First(dbbin, b.ID).Error
		if err != nil {
			return err
		}

		// Find size
		// Blacklist
		grp, err := d.FindGroupByName(b.Group)
		if err != nil {
			logrus.Errorf("Unknown group %s for binary %d: %s", b.Group, b.ID, b.Name)
		}
		nzbstr, err := nzb.WriteNZB(dbbin)
		if err != nil {
			return err
		}
		newrel := &types.Release{
			Name:         b.Name,
			OriginalName: b.Name,
			SearchName:   cleanReleaseName(b.Name),
			Posted:       b.Posted,
			From:         b.From,
			Group:        *grp,
			Size:         dbbin.Size(),
			NZB:          nzbstr,
		}

		// Categorize

		// Check if size is too small
		// Check if too few files
		tx := d.DB.Begin()
		err = tx.Save(newrel).Error
		if err != nil {
			tx.Rollback()
			return err
		}
		partids := make([]int64, len(dbbin.Parts))
		for i, p := range dbbin.Parts {
			partids[i] = p.ID
		}
		err = tx.Where("binary_id = ?", dbbin.ID).Delete(types.Part{}).Error
		if err != nil {
			tx.Rollback()
			return err
		}

		err = tx.Where("part_id in (?)", partids).Delete(types.Segment{}).Error
		if err != nil {
			tx.Rollback()
			return err
		}

		err = tx.Delete(dbbin).Error
		if err != nil {
			tx.Rollback()
			return err
		}
		tx.Commit()
	}
	return nil
}
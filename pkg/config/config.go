package config

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vesoft-inc/nebula-importer/pkg/base"
	ierrors "github.com/vesoft-inc/nebula-importer/pkg/errors"
	"github.com/vesoft-inc/nebula-importer/pkg/logger"
	yaml "gopkg.in/yaml.v2"
)

type NebulaClientConnection struct {
	User     *string `json:"user" yaml:"user"`
	Password *string `json:"password" yaml:"password"`
	Address  *string `json:"address" yaml:"address"`
}

type NebulaPostStart struct {
	Commands    *string `json:"commands" yaml:"commands"`
	AfterPeriod *string `json:"afterPeriod" yaml:"afterPeriod"`
}

type NebulaPreStop struct {
	Commands *string `json:"commands" yaml:"commands"`
}

type NebulaClientSettings struct {
	Retry             *int                    `json:"retry" yaml:"retry"`
	Concurrency       *int                    `json:"concurrency" yaml:"concurrency"`
	ChannelBufferSize *int                    `json:"channelBufferSize" yaml:"channelBufferSize"`
	Space             *string                 `json:"space" yaml:"space"`
	Connection        *NebulaClientConnection `json:"connection" yaml:"connection"`
	PostStart         *NebulaPostStart        `json:"postStart" yaml:"postStart"` // from v1
	PreStop           *NebulaPreStop          `json:"preStop" yaml:"preStop"`     // from v1
}

type Prop struct {
	Name  *string `json:"name" yaml:"name"`
	Type  *string `json:"type" yaml:"type"`
	Index *int    `json:"index" yaml:"index"`
}

type VID struct {
	Index    *int    `json:"index" yaml:"index"`
	Function *string `json:"function" yaml:"function"`
	Type     *string `json:"type" yaml:"type"`
}

type Rank struct {
	Index *int `json:"index" yaml:"index"`
}

type Edge struct {
	Name        *string `json:"name" yaml:"name"`
	WithRanking *bool   `json:"withRanking" yaml:"withRanking"`
	Props       []*Prop `json:"props" yaml:"props"`
	SrcVID      *VID    `json:"srcVID" yaml:"srcVID"`
	DstVID      *VID    `json:"dstVID" yaml:"dstVID"`
	Rank        *Rank   `json:"rank" yaml:"rank"`
}

type Tag struct {
	Name  *string `json:"name" yaml:"name"`
	Props []*Prop `json:"props" yaml:"props"`
}

type Vertex struct {
	VID  *VID   `json:"vid" yaml:"vid"`
	Tags []*Tag `json:"tags" yaml:"tags"`
}

type Schema struct {
	Type   *string `json:"type" yaml:"type"`
	Edge   *Edge   `json:"edge" yaml:"edge"`
	Vertex *Vertex `json:"vertex" yaml:"vertex"`
}

type CSVConfig struct {
	WithHeader *bool   `json:"withHeader" yaml:"withHeader"`
	WithLabel  *bool   `json:"withLabel" yaml:"withLabel"`
	Delimiter  *string `json:"delimiter" yaml:"delimiter"`
}

type File struct {
	Path         *string    `json:"path" yaml:"path"`
	FailDataPath *string    `json:"failDataPath" yaml:"failDataPath"`
	BatchSize    *int       `json:"batchSize" yaml:"batchSize"`
	Limit        *int       `json:"limit" yaml:"limit"`
	InOrder      *bool      `json:"inOrder" yaml:"inOrder"`
	Type         *string    `json:"type" yaml:"type"`
	CSV          *CSVConfig `json:"csv" yaml:"csv"`
	Schema       *Schema    `json:"schema" yaml:"schema"`
}

type YAMLConfig struct {
	Version              *string               `json:"version" yaml:"version"`
	Description          *string               `json:"description" yaml:"description"`
	RemoveTempFiles      *bool                 `json:"removeTempFiles" yaml:"removeTempFiles"` // from v1
	NebulaClientSettings *NebulaClientSettings `json:"clientSettings" yaml:"clientSettings"`
	LogPath              *string               `json:"logPath" yaml:"logPath"`
	Files                []*File               `json:"files" yaml:"files"`
}

var (
	kDefaultVidType   = "string"
	kDefaultConnAddr  = "127.0.0.1:9669"
	kDefaultUser      = "root"
	kDefaultPassword  = "nebula"
	kDefaultBatchSize = 128
	supportedVersions = []string{"v1rc1", "v1rc2", "v1", "v2"}
)

func isSupportedVersion(ver string) bool {
	for _, v := range supportedVersions {
		if v == ver {
			return true
		}
	}
	return false
}

func Parse(filename string) (*YAMLConfig, error) {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, ierrors.Wrap(ierrors.InvalidConfigPathOrFormat, err)
	}

	var conf YAMLConfig
	if err = yaml.Unmarshal(content, &conf); err != nil {
		return nil, ierrors.Wrap(ierrors.InvalidConfigPathOrFormat, err)
	}

	if conf.Version == nil && !isSupportedVersion(*conf.Version) {
		return nil, ierrors.Wrap(ierrors.InvalidConfigPathOrFormat,
			fmt.Errorf("The supported YAML configure versions are %v, please upgrade importer.", supportedVersions))
	}
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, ierrors.Wrap(ierrors.InvalidConfigPathOrFormat, err)
	}
	path := filepath.Dir(abs)
	if err = conf.ValidateAndReset(path); err != nil {
		return nil, ierrors.Wrap(ierrors.ConfigError, err)
	}

	return &conf, nil
}

func (config *YAMLConfig) ValidateAndReset(dir string) error {
	if config.NebulaClientSettings == nil {
		return errors.New("please configure clientSettings")
	}
	if err := config.NebulaClientSettings.validateAndReset("clientSettings"); err != nil {
		return err
	}

	if config.RemoveTempFiles == nil {
		removeTempFiles := false
		config.RemoveTempFiles = &removeTempFiles
		logger.Warnf("You have not configured whether to remove generated temporary files, reset to default value. removeTempFiles: %v",
			*config.RemoveTempFiles)
	}

	if config.LogPath == nil {
		defaultPath := filepath.Join(os.TempDir(), fmt.Sprintf("nebula-importer-%d.log", time.Now().UnixNano()))
		config.LogPath = &defaultPath
		logger.Warnf("You have not configured the log file path in: logPath, reset to default path: %s", *config.LogPath)
	}
	if !filepath.IsAbs(*config.LogPath) {
		absPath := filepath.Join(dir, *config.LogPath)
		config.LogPath = &absPath
	}

	if config.Files == nil || len(config.Files) == 0 {
		return errors.New("There is no files in configuration")
	}

	//TODO(yuyu): check each item in config.Files
	// if item is a directory, iter this directory and replace this directory config section by filename config section
	if err := config.expandDirectoryToFiles(dir); err != nil {
		logger.Errorf("%s", err)
		return err
	}
	for i := range config.Files {
		if err := config.Files[i].validateAndReset(dir, fmt.Sprintf("files[%d]", i)); err != nil {
			return err
		}
	}

	return nil
}

func (config *YAMLConfig) expandDirectoryToFiles(dir string) (err error) {
	var newFiles []*File

	for _, file := range config.Files {
		err, files := file.expandFiles(dir)
		if err != nil {
			logger.Errorf("error when expand file: %s", err)
			return err
		}
		for _, f := range files {
			newFiles = append(newFiles, f)
		}
	}
	config.Files = newFiles

	return err
}

func (n *NebulaPostStart) validateAndReset(prefix string) error {
	if n.AfterPeriod != nil {
		_, err := time.ParseDuration(*n.AfterPeriod)
		if err != nil {
			return err
		}
	} else {
		period := "0s"
		n.AfterPeriod = &period
	}

	return nil
}

func (n *NebulaClientSettings) validateAndReset(prefix string) error {
	if n.Space == nil {
		return fmt.Errorf("Please configure the space name in: %s.space", prefix)
	}

	if n.Retry == nil {
		retry := 1
		n.Retry = &retry
		logger.Warnf("Invalid retry option in %s.retry, reset to %d ", prefix, *n.Retry)
	}

	if n.Concurrency == nil {
		d := 10
		n.Concurrency = &d
		logger.Warnf("Invalid client concurrency in %s.concurrency, reset to %d", prefix, *n.Concurrency)
	}

	if n.ChannelBufferSize == nil {
		d := 128
		n.ChannelBufferSize = &d
		logger.Warnf("Invalid client channel buffer size in %s.channelBufferSize, reset to %d", prefix, *n.ChannelBufferSize)
	}

	if n.Connection == nil {
		return fmt.Errorf("Please configure the connection information in: %s.connection", prefix)
	}
	if err := n.Connection.validateAndReset(fmt.Sprintf("%s.connection", prefix)); err != nil {
		return err
	}

	if n.PostStart != nil {
		return n.PostStart.validateAndReset(fmt.Sprintf("%s.postStart", prefix))
	}
	return nil
}

func (c *NebulaClientConnection) validateAndReset(prefix string) error {
	if c.Address == nil {
		c.Address = &kDefaultConnAddr
		logger.Warnf("%s.address: %s", prefix, *c.Address)
	}

	if c.User == nil {
		c.User = &kDefaultUser
		logger.Warnf("%s.user: %s", prefix, *c.User)
	}

	if c.Password == nil {
		c.Password = &kDefaultPassword
		logger.Warnf("%s.password: %s", prefix, *c.Password)
	}
	return nil
}

func (f *File) IsInOrder() bool {
	return (f.InOrder != nil && *f.InOrder) || (f.CSV != nil && f.CSV.WithLabel != nil && *f.CSV.WithLabel)
}

func (f *File) validateAndReset(dir, prefix string) error {
	if f.Path == nil {
		return fmt.Errorf("Please configure file path in: %s.path", prefix)
	}

	if base.HasHttpPrefix(*f.Path) {
		if _, err := url.ParseRequestURI(*f.Path); err != nil {
			return err
		}

		if _, _, err := base.ExtractFilename(*f.Path); err != nil {
			return err
		}

		if f.FailDataPath == nil {
			failDataPath := filepath.Join(os.TempDir(), fmt.Sprintf("nebula-importer-err-data-%d", time.Now().UnixNano()))
			f.FailDataPath = &failDataPath
			logger.Warnf("You have not configured the failed data output file path in: %s.failDataPath, reset to tmp path: %s",
				prefix, *f.FailDataPath)
		}
	} else {
		if !filepath.IsAbs(*f.Path) {
			absPath := filepath.Join(dir, *f.Path)
			f.Path = &absPath
		}
		if !base.FileExists(*f.Path) {
			return fmt.Errorf("File(%s) doesn't exist", *f.Path)
		}

		if f.FailDataPath == nil {
			p := filepath.Join(filepath.Dir(*f.Path), "err", filepath.Base(*f.Path))
			f.FailDataPath = &p
			logger.Warnf("You have not configured the failed data output file path in: %s.failDataPath, reset to default path: %s",
				prefix, *f.FailDataPath)
		} else {
			if !filepath.IsAbs(*f.FailDataPath) {
				absPath := filepath.Join(dir, *f.FailDataPath)
				f.FailDataPath = &absPath
			}
		}
	}

	if f.BatchSize == nil {
		f.BatchSize = &kDefaultBatchSize
		logger.Infof("Invalid batch size in file(%s), reset to %d", *f.Path, *f.BatchSize)
	}

	if f.InOrder == nil {
		inOrder := false
		f.InOrder = &inOrder
	}

	if strings.ToLower(*f.Type) != "csv" {
		// TODO: Now only support csv import
		return fmt.Errorf("Invalid file data type: %s, reset to csv", *f.Type)
	}

	if f.CSV != nil {
		err := f.CSV.validateAndReset(fmt.Sprintf("%s.csv", prefix))
		if err != nil {
			return err
		}
	}

	if f.Schema == nil {
		return fmt.Errorf("Please configure file schema: %s.schema", prefix)
	}
	return f.Schema.validateAndReset(fmt.Sprintf("%s.schema", prefix))
}

func (f *File) expandFiles(dir string) (err error, files []*File) {
	if base.HasHttpPrefix(*f.Path) {
		files = append(files, f)
	} else {
		if !filepath.IsAbs(*f.Path) {
			absPath := filepath.Join(dir, *f.Path)
			f.Path = &absPath
		}

		fileNames, err := filepath.Glob(*f.Path)
		if err != nil || len(fileNames) == 0 {
			logger.Errorf("error file path: %s", *f.Path)
			return err, files
		}

		if len(fileNames) == 1 {
			files = append(files, f)
			return err, files
		}

		for i := range fileNames {
			var failedDataPath *string = nil
			if f.FailDataPath != nil {
				base := filepath.Base(fileNames[i])
				tmp := filepath.Join(*f.FailDataPath, base)
				failedDataPath = &tmp
				logger.Infof("Failed data path: %v", *failedDataPath)
			}
			eachConf := *f
			eachConf.Path = &fileNames[i]
			eachConf.FailDataPath = failedDataPath
			files = append(files, &eachConf)
			logger.Infof("find file: %v", *eachConf.Path)
		}
	}

	return err, files
}

func (c *CSVConfig) validateAndReset(prefix string) error {
	if c.WithHeader == nil {
		h := false
		c.WithHeader = &h
		logger.Infof("%s.withHeader: %v", prefix, false)
	}

	if c.WithLabel == nil {
		l := false
		c.WithLabel = &l
		logger.Infof("%s.withLabel: %v", prefix, false)
	}

	if c.Delimiter != nil {
		if len(*c.Delimiter) == 0 {
			return fmt.Errorf("%s.delimiter is empty string", prefix)
		}
	}

	return nil
}

func (s *Schema) IsVertex() bool {
	return strings.ToUpper(*s.Type) == "VERTEX"
}

func (s *Schema) String() string {
	if s.IsVertex() {
		return s.Vertex.String()
	} else {
		return s.Edge.String()
	}
}

func (s *Schema) CollectEmptyPropsTagNames() []string {
	if !s.IsVertex() || s.Vertex == nil {
		return nil
	}
	var tagNames []string
	for _, tag := range s.Vertex.Tags {
		if len(tag.Props) == 0 {
			tagNames = append(tagNames, *tag.Name)
			continue
		}
		for _, prop := range tag.Props {
			if prop != nil {
				continue
			}
		}
		tagNames = append(tagNames, *tag.Name)
	}
	return tagNames
}

func (s *Schema) validateAndReset(prefix string) error {
	var err error = nil
	switch strings.ToLower(*s.Type) {
	case "edge":
		if s.Edge != nil {
			err = s.Edge.validateAndReset(fmt.Sprintf("%s.edge", prefix))
		} else {
			logger.Infof("%s.edge is nil", prefix)
		}
	case "vertex":
		if s.Vertex != nil {
			err = s.Vertex.validateAndReset(fmt.Sprintf("%s.vertex", prefix))
		} else {
			logger.Infof("%s.vertex is nil", prefix)
		}
	default:
		err = fmt.Errorf("Error schema type(%s) in %s.type only edge and vertex are supported", *s.Type, prefix)
	}
	return err
}

func (v *VID) ParseFunction(str string) (err error) {
	i := strings.Index(str, "(")
	j := strings.Index(str, ")")
	err = nil
	if i < 0 && j < 0 {
		v.Function = nil
		v.Type = &kDefaultVidType
	} else if i > 0 && j > i {
		strs := strings.ToLower(str[i+1 : j])
		fnType := strings.Split(strs, "+")
		if len(fnType) > 1 {
			v.Function = &fnType[0]
			v.Type = &fnType[1]
		} else {
			v.Function = nil
			v.Type = &fnType[0]
		}
	} else {
		err = fmt.Errorf("Invalid function format: %s", str)
	}
	return
}

func (v *VID) String(vid string) string {
	if v.Function == nil || *v.Function == "" {
		return fmt.Sprintf("%s(%s)", vid, *v.Type)
	} else {
		return fmt.Sprintf("%s(%s+%s)", vid, *v.Function, *v.Type)
	}
}

func (v *VID) FormatValue(record base.Record) (string, error) {
	if len(record) <= *v.Index {
		return "", fmt.Errorf("vid index(%d) out of range record length(%d)", *v.Index, len(record))
	}
	if v.Function == nil || *v.Function == "" {
		vid := record[*v.Index]
		if err := checkVidFormat(vid, *v.Type == "int"); err != nil {
			return "", err
		}
		if *v.Type == "string" {
			return fmt.Sprintf("%q", vid), nil
		} else {
			return vid, nil
		}
	} else {
		return fmt.Sprintf("%s(%q)", *v.Function, record[*v.Index]), nil
	}
}

func (v *VID) checkFunction(prefix string) error {
	if v.Function != nil {
		switch strings.ToLower(*v.Function) {
		// FIXME: uuid is not supported in nebula-graph-v2, and hash returns int which is not the valid vid type.
		case "", "hash", "uuid":
		default:
			return fmt.Errorf("Invalid %s.function: %s, only following values are supported: \"\", hash, uuid", prefix, *v.Function)
		}
	}
	return nil
}

func (v *VID) validateAndReset(prefix string, defaultVal int) error {
	if v.Index == nil {
		v.Index = &defaultVal
	}
	if *v.Index < 0 {
		return fmt.Errorf("Invalid %s.index: %d", prefix, *v.Index)
	}
	if err := v.checkFunction(prefix); err != nil {
		return err
	}
	if v.Type != nil {
		vidType := strings.TrimSpace(strings.ToLower(*v.Type))
		if vidType != "string" && vidType != "int" {
			return fmt.Errorf("vid type must be `string' or `int', now is %s", vidType)
		}
	} else {
		v.Type = &kDefaultVidType
		logger.Warnf("Not set %s.Type, reset to default value `%s'", prefix, *v.Type)
	}
	return nil
}

func (r *Rank) validateAndReset(prefix string, defaultVal int) error {
	if r.Index == nil {
		r.Index = &defaultVal
	}
	if *r.Index < 0 {
		return fmt.Errorf("Invalid %s.index: %d", prefix, *r.Index)
	}
	return nil
}

var re = regexp.MustCompile(`^(0[xX][0-9a-fA-F]+|0[0-7]+|[+-]?\d+|hash\(".+"\)|uuid\(".+"\))$`)

func checkVidFormat(vid string, isInt bool) error {
	if isInt && !re.MatchString(vid) {
		return fmt.Errorf("Invalid vid format: %s", vid)
	}
	return nil
}

func (e *Edge) FormatValues(record base.Record) (string, error) {
	var cells []string
	for i, prop := range e.Props {
		if c, err := prop.FormatValue(record); err != nil {
			return "", fmt.Errorf("edge: %s, column: %d, error: %v", e.String(), i, err)
		} else {
			cells = append(cells, c)
		}
	}
	rank := ""
	if e.Rank != nil && e.Rank.Index != nil {
		rank = fmt.Sprintf("@%s", record[*e.Rank.Index])
	}
	srcVID, err := e.SrcVID.FormatValue(record)
	if err != nil {
		return "", err
	}
	dstVID, err := e.DstVID.FormatValue(record)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(" %s->%s%s:(%s) ", srcVID, dstVID, rank, strings.Join(cells, ",")), nil
}

func (e *Edge) maxIndex() int {
	maxIdx := 0
	if e.SrcVID != nil && e.SrcVID.Index != nil && *e.SrcVID.Index > maxIdx {
		maxIdx = *e.SrcVID.Index
	}

	if e.DstVID != nil && e.DstVID.Index != nil && *e.DstVID.Index > maxIdx {
		maxIdx = *e.DstVID.Index
	}

	if e.Rank != nil && e.Rank.Index != nil && *e.Rank.Index > maxIdx {
		maxIdx = *e.Rank.Index
	}

	for _, p := range e.Props {
		if p != nil && p.Index != nil && *p.Index > maxIdx {
			maxIdx = *p.Index
		}
	}

	return maxIdx
}

func combine(cell, val string) string {
	if len(cell) > 0 {
		return fmt.Sprintf("%s/%s", cell, val)
	} else {
		return val
	}
}

func (e *Edge) String() string {
	cells := make([]string, e.maxIndex()+1)
	if e.SrcVID != nil && e.SrcVID.Index != nil {
		cells[*e.SrcVID.Index] = combine(cells[*e.SrcVID.Index], e.SrcVID.String(base.LABEL_SRC_VID))
	}
	if e.DstVID != nil && e.DstVID.Index != nil {
		cells[*e.DstVID.Index] = combine(cells[*e.DstVID.Index], e.DstVID.String(base.LABEL_DST_VID))
	}
	if e.Rank != nil && e.Rank.Index != nil {
		cells[*e.Rank.Index] = combine(cells[*e.Rank.Index], base.LABEL_RANK)
	}
	for _, prop := range e.Props {
		if prop.Index != nil {
			cells[*prop.Index] = combine(cells[*prop.Index], prop.String(*e.Name))
		}
	}
	for i := range cells {
		if cells[i] == "" {
			cells[i] = base.LABEL_IGNORE
		}
	}
	return strings.Join(cells, ",")
}

func (e *Edge) validateAndReset(prefix string) error {
	if e.Name == nil {
		return fmt.Errorf("Please configure edge name in: %s.name", prefix)
	}
	if e.SrcVID != nil {
		if err := e.SrcVID.validateAndReset(fmt.Sprintf("%s.srcVID", prefix), 0); err != nil {
			return err
		}
	} else {
		index := 0
		e.SrcVID = &VID{Index: &index, Type: &kDefaultVidType}
	}
	if e.DstVID != nil {
		if err := e.DstVID.validateAndReset(fmt.Sprintf("%s.dstVID", prefix), 1); err != nil {
			return err
		}
	} else {
		index := 1
		e.DstVID = &VID{Index: &index, Type: &kDefaultVidType}
	}
	start := 2
	if e.Rank != nil {
		if err := e.Rank.validateAndReset(fmt.Sprintf("%s.rank", prefix), 2); err != nil {
			return err
		}
		start++
	} else {
		if e.WithRanking != nil && *e.WithRanking {
			index := 2
			e.Rank = &Rank{Index: &index}
			start++
		}
	}
	for i := range e.Props {
		if e.Props[i] != nil {
			if err := e.Props[i].validateAndReset(fmt.Sprintf("%s.prop[%d]", prefix, i), i+start); err != nil {
				return err
			}
		} else {
			logger.Errorf("prop %d of edge %s is nil", i, *e.Name)
		}
	}
	return nil
}

func (v *Vertex) FormatValues(record base.Record) (string, error) {
	var cells []string
	for _, tag := range v.Tags {
		str, noProps, err := tag.FormatValues(record)
		if err != nil {
			return "", err
		}
		if !noProps {
			cells = append(cells, str)
		}
	}
	vid, err := v.VID.FormatValue(record)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(" %s: (%s)", vid, strings.Join(cells, ",")), nil
}

func (v *Vertex) maxIndex() int {
	maxIdx := 0
	if v.VID != nil && v.VID.Index != nil && *v.VID.Index > maxIdx {
		maxIdx = *v.VID.Index
	}
	for _, tag := range v.Tags {
		if tag != nil {
			for _, prop := range tag.Props {
				if prop != nil && prop.Index != nil && *prop.Index > maxIdx {
					maxIdx = *prop.Index
				}
			}
		}
	}

	return maxIdx
}

func (v *Vertex) String() string {
	cells := make([]string, v.maxIndex()+1)
	if v.VID != nil && v.VID.Index != nil {
		cells[*v.VID.Index] = v.VID.String(base.LABEL_VID)
	}
	for _, tag := range v.Tags {
		for _, prop := range tag.Props {
			if prop != nil && prop.Index != nil {
				cells[*prop.Index] = combine(cells[*prop.Index], prop.String(*tag.Name))
			}
		}
	}

	for i := range cells {
		if cells[i] == "" {
			cells[i] = base.LABEL_IGNORE
		}
	}
	return strings.Join(cells, ",")
}

func (v *Vertex) validateAndReset(prefix string) error {
	// if v.Tags == nil {
	// 	return fmt.Errorf("Please configure %.tags", prefix)
	// }
	if v.VID != nil {
		if err := v.VID.validateAndReset(fmt.Sprintf("%s.vid", prefix), 0); err != nil {
			return err
		}
	} else {
		index := 0
		v.VID = &VID{Index: &index, Type: &kDefaultVidType}
	}
	j := 1
	for i := range v.Tags {
		if v.Tags[i] != nil {
			if err := v.Tags[i].validateAndReset(fmt.Sprintf("%s.tags[%d]", prefix, i), j); err != nil {
				return err
			}
			j = j + len(v.Tags[i].Props)
		} else {
			logger.Errorf("tag %d is nil", i)
		}
	}
	return nil
}

func (p *Prop) IsStringType() bool {
	return strings.ToLower(*p.Type) == "string"
}

func (p *Prop) IsDateOrTimeType() bool {
	t := strings.ToLower(*p.Type)
	return t == "date" || t == "time" || t == "datetime" || t == "timestamp"
}

func (p *Prop) IsGeographyType() bool {
	t := strings.ToLower(*p.Type)
	return t == "geography" || t == "geography(point)" || t == "geography(linestring)" || t == "geography(polygon)"
}

func (p *Prop) FormatValue(record base.Record) (string, error) {
	if p.Index != nil && *p.Index >= len(record) {
		return "", fmt.Errorf("Prop index %d out range %d of record(%v)", *p.Index, len(record), record)
	}
	r := record[*p.Index]
	if p.IsStringType() {
		return fmt.Sprintf("%q", r), nil
	}
	if p.IsDateOrTimeType() {
		return fmt.Sprintf("%s(%q)", strings.ToLower(*p.Type), r), nil
	}
	// Only support wkt for geography currently
	if p.IsGeographyType() {
		return fmt.Sprintf("ST_GeogFromText(%q)", r), nil
	}

	return r, nil
}

func (p *Prop) String(prefix string) string {
	return fmt.Sprintf("%s.%s:%s", prefix, *p.Name, *p.Type)
}

func (p *Prop) validateAndReset(prefix string, val int) error {
	*p.Type = strings.ToLower(*p.Type)
	if !base.IsValidType(*p.Type) {
		return fmt.Errorf("Error property type of %s.type: %s", prefix, *p.Type)
	}
	if p.Index == nil {
		p.Index = &val
	} else {
		if *p.Index < 0 {
			return fmt.Errorf("Invalid prop index: %d, name: %s, type: %s", *p.Index, *p.Name, *p.Type)
		}
	}
	return nil
}

func (t *Tag) FormatValues(record base.Record) (string, bool, error) {
	var cells []string
	noProps := true
	for _, p := range t.Props {
		if p == nil {
			continue
		}
		noProps = false
		if c, err := p.FormatValue(record); err != nil {
			return "", noProps, fmt.Errorf("tag: %v, error: %v", *t, err)
		} else {
			cells = append(cells, c)
		}
	}
	return strings.Join(cells, ","), noProps, nil
}

func (t *Tag) validateAndReset(prefix string, start int) error {
	if t.Name == nil {
		return fmt.Errorf("Please configure the vertex tag name in: %s.name", prefix)
	}

	for i := range t.Props {
		if t.Props[i] != nil {
			if err := t.Props[i].validateAndReset(fmt.Sprintf("%s.props[%d]", prefix, i), i+start); err != nil {
				return err
			}
		} else {
			logger.Errorf("prop %d of tag %s is nil", i, *t.Name)
		}
	}
	return nil
}

package eudore

import (
	"bufio"
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 定义日志级别
const (
	LoggerDebug LoggerLevel = iota
	LoggerInfo
	LoggerWarning
	LoggerError
	LoggerFatal
)

/*
Logger 定义日志输出接口实现下列功能:
	五级日志格式化输出
	日志条目带Fields属性
	json有序格式化输出
	日志器初始化前日志处理
	文件行信息输出
	默认输入文件切割并软连接。
*/
type Logger interface {
	Debug(...interface{})
	Info(...interface{})
	Warning(...interface{})
	Error(...interface{})
	Fatal(...interface{})
	Debugf(string, ...interface{})
	Infof(string, ...interface{})
	Warningf(string, ...interface{})
	Errorf(string, ...interface{})
	Fatalf(string, ...interface{})
	WithField(string, interface{}) Logger
	WithFields([]string, []interface{}) Logger
	GetLevel() LoggerLevel
	SetLevel(LoggerLevel)
	Sync() error
}

// LoggerLevel 定义日志级别
type LoggerLevel int32

// loggerInitHandler 定义初始日志处理器必要接口，使用新日志处理器处理当前记录的全部日志。
type loggerInitHandler interface {
	NextHandler(Logger)
}

// LoggerStdConfig 定义loggerStd配置信息。
//
// Writer 设置日志输出流，如果为空会使用Std和Path创建一个LoggerWriter。
//
// Std 是否输出日志到os.Stdout标准输出流。
//
// Path 指定文件输出路径,如果为空强制指定Std为true。
//
// MaxSize 指定文件切割大小，需要Path中存在index字符串,用于替换成切割文件索引。
//
// Link 如果非空会作为软连接的目标路径。
//
// Level 日志输出级别。
//
// TimeFormat 日志输出时间格式化格式。
//
// FileLine 是否输出调用日志输出的函数和文件位置
type LoggerStdConfig struct {
	Writer     LoggerWriter `json:"-" xml:"-" alias:"writer" description:"Logger output writer."`
	Std        bool         `json:"std" xml:"std" alias:"std" description:"Is output to os.Stdout."`
	Path       string       `json:"path" xml:"path" alias:"path" description:"Output logger file path."`
	MaxSize    uint64       `json:"maxsize" xml:"maxsize" alias:"maxsize" description:"Output file max size, 'Path' must contain 'index'."`
	Link       string       `json:"link" xml:"link" alias:"link" description:"Output file link to path."`
	Level      LoggerLevel  `json:"level" xml:"level" alias:"level" description:"Logger Output level."`
	TimeFormat string       `json:"timeformat" xml:"timeformat" alias:"timeformat" description:"Logger output timeFormat, default '2006-01-02 15:04:05'"`
	FileLine   bool         `json:"fileline" xml:"fileline" alias:"fileline" description:"Is output file and line."`
}

// LoggerStd 定义日志默认实现条目信息。
type LoggerStd struct {
	LoggerStdData
	// enrty data
	Time       time.Time
	Message    string
	Keys       []string
	Vals       []interface{}
	Buffer     []byte
	Timeformat string
	// 日志标识 true是Logger false是Entry
	Logger bool
	Level  LoggerLevel
	Depth  int
}

// LoggerStdData 定义loggerStd的数据存储
type LoggerStdData interface {
	GetLogger() *LoggerStd
	PutLogger(*LoggerStd)
	Sync() error
}

// NewLoggerStd 创建一个标准日志处理器。
//
// 参数为一个eudore.LoggerStdConfig或map保存的创建配置,配置选项含义参考eudore.LoggerStdConfig说明。
func NewLoggerStd(arg interface{}) Logger {
	// 解析配置
	data, ok := arg.(LoggerStdData)
	if !ok {
		data = NewLoggerStdDataJSON(arg)
	}
	log := data.GetLogger()
	log.Logger = true
	return log
}

// NewLoggerWithContext 方法从环境上下文ContextKeyLogger获取Logger，如果无法获取Logger返回DefaultLoggerNull对象。
func NewLoggerWithContext(ctx context.Context) Logger {
	log, ok := ctx.Value(ContextKeyLogger).(Logger)
	if ok {
		return log
	}
	return DefaultLoggerNull
}

// NewLoggerInit The initial log processor only records logs, and gets a new Logger to process logs when Unmount.
//
// NewLoggerInit 初始日志处理器仅记录日志，在Unmount时获取新Logger处理日志.
func NewLoggerInit() Logger {
	return NewLoggerStd(&loggerStdDataInit{})
}

// NewLoggerNull 定义空日志输出，丢弃所有日志。
func NewLoggerNull() Logger {
	return NewLoggerStd(&loggerStdDataNull{})
}

// NewLoggerStdDataJSON 函数创建一个LoggerStd的JSON数据处理器。
func NewLoggerStdDataJSON(arg interface{}) LoggerStdData {
	config := &LoggerStdConfig{
		TimeFormat: DefaultLoggerTimeFormat,
	}
	ConvertTo(arg, config)
	logdepath := 3
	if config.FileLine {
		logdepath |= 0x100
	}
	if config.Writer == nil {
		var err error
		config.Path = strings.TrimSpace(config.Path)
		config.Writer, err = NewLoggerWriterRotate(config.Path, config.Std, config.MaxSize, newLoggerLinkName(config.Link))
		if err != nil {
			panic(err)
		}
	}

	data := &loggerStdDataJSON{
		LoggerWriter: config.Writer,
	}
	data.Pool.New = func() interface{} {
		return &LoggerStd{
			LoggerStdData: data,
			Level:         config.Level,
			Buffer:        make([]byte, 0, 2048),
			Keys:          make([]string, 0, 4),
			Vals:          make([]interface{}, 0, 4),
			Timeformat:    config.TimeFormat,
			Depth:         logdepath,
		}
	}
	return data
}

type loggerStdDataJSON struct {
	LoggerWriter
	sync.Mutex
	sync.Pool
	done chan struct{}
}

// Mount 方法启动周期Sync，每80ms执行一次。
func (data *loggerStdDataJSON) Mount(ctx context.Context) {
	data.Lock()
	defer data.Unlock()
	if data.done == nil {
		data.done = make(chan struct{})
	}
	go func() {
		ticker := time.NewTicker(DefaultLoggerSyncDuration)
		for {
			select {
			case <-data.done:
				ticker.Stop()
				close(data.done)
				data.done = nil
				return
			case <-ticker.C:
				data.Sync()
			}
		}
	}()
}

// Unmount 方法关闭周期Sync。
func (data *loggerStdDataJSON) Unmount(ctx context.Context) {
	data.Lock()
	defer data.Unlock()
	if data.done != nil {
		data.done <- struct{}{}
	}
	data.LoggerWriter.Sync()
}

func (data *loggerStdDataJSON) Sync() error {
	data.Lock()
	defer data.Unlock()
	return data.LoggerWriter.Sync()
}

func (data *loggerStdDataJSON) GetLogger() *LoggerStd {
	return data.Get().(*LoggerStd)
}

func (data *loggerStdDataJSON) PutLogger(entry *LoggerStd) {
	if len(entry.Message) > 0 || len(entry.Keys) > 0 {
		switch entry.Depth >> 8 {
		case 1:
			name, file, line := logFormatNameFileLine(entry.Depth & 0xff)
			entry.Keys = append(entry.Keys, "name", "file", "line")
			entry.Vals = append(entry.Vals, name, file, line)
		case 2, 3:
			entry.Keys = append(entry.Keys, "stack")
			entry.Vals = append(entry.Vals, GetPanicStack(entry.Depth&0xff+1))
		}
		if len(entry.Keys) > len(entry.Vals) {
			entry.Keys = entry.Keys[0:len(entry.Vals)]
			entry.WithField("loggererr", "LoggerStd.loggerStdDataJSON: The number of field keys and values are not equal")
		}
		loggerEntryStdFormat(entry)
		data.Lock()
		data.Write(entry.Buffer)
		data.Unlock()
		entry.Message = ""
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
		entry.Buffer = entry.Buffer[0:0]
	}
	data.Put(entry)
}

type loggerStdDataInit struct {
	sync.Mutex
	Data []*LoggerStd
}

func (data *loggerStdDataInit) GetLogger() *LoggerStd {
	return &LoggerStd{
		LoggerStdData: data,
	}
}
func (data *loggerStdDataInit) PutLogger(entry *LoggerStd) {
	entry.Time = time.Now()
	data.Lock()
	data.Data = append(data.Data, entry)
	data.Unlock()
}

// Unmount 方法获取ContextKeyLogger.(Logger)接受Init存储的日志。
func (data *loggerStdDataInit) Unmount(ctx context.Context) {
	data.Lock()
	defer data.Unlock()
	logger, _ := ctx.Value(ContextKeyLogger).(Logger)
	if logger == nil {
		logger = NewLoggerStd(nil)
	}

	logger = logger.WithField("depth", "disable").WithField("logger", true)
	for _, data := range data.Data {
		entry := logger.WithField("time", data.Time)
		for i := range data.Keys {
			entry = entry.WithField(data.Keys[i], data.Vals[i])
		}
		switch data.Level {
		case LoggerDebug:
			entry.Debug(data.Message)
		case LoggerInfo:
			entry.Info(data.Message)
		case LoggerWarning:
			entry.Warning(data.Message)
		case LoggerError:
			entry.Error(data.Message)
		case LoggerFatal:
			entry.Fatal(data.Message)
		}
	}
	data.Data = data.Data[0:0]
	logger.Sync()
}

func (data *loggerStdDataInit) Sync() error {
	return nil
}

type loggerStdDataNull struct{}

func (data *loggerStdDataNull) GetLogger() *LoggerStd {
	return &LoggerStd{
		LoggerStdData: data,
	}
}

func (data *loggerStdDataNull) PutLogger(entry *LoggerStd) {
}

func (data *loggerStdDataNull) Sync() error {
	return nil
}

func (entry *LoggerStd) getEntry() *LoggerStd {
	newentry := entry.LoggerStdData.GetLogger()
	newentry.Level = entry.Level
	newentry.Depth = entry.Depth
	if len(entry.Keys) != 0 {
		newentry.Keys = append(newentry.Keys, entry.Keys...)
		newentry.Vals = append(newentry.Vals, entry.Vals...)
	}
	return newentry
}

// Mount 方法使LoggerStd挂载上下文，上下文传递给LoggerStdData。
func (entry *LoggerStd) Mount(ctx context.Context) {
	withMount(ctx, entry.LoggerStdData)
}

// Unmount 方法使LoggerStd卸载上下文，上下文传递给LoggerStdData。
func (entry *LoggerStd) Unmount(ctx context.Context) {
	withUnmount(ctx, entry.LoggerStdData)
}

// Metadata 方法从LoggerStdData获取元数据返回。
func (entry *LoggerStd) Metadata() interface{} {
	return withMetadata(entry.LoggerStdData)
}

// GetLevel 方法获取当前日志输出级别，判断级别取消日志生成。
func (entry *LoggerStd) GetLevel() LoggerLevel {
	return entry.Level
}

// SetLevel 方法设置当前日志输出级别。
func (entry *LoggerStd) SetLevel(level LoggerLevel) {
	entry.Level = level
}

// Sync 方法将缓冲写入到输出流。
func (entry *LoggerStd) Sync() error {
	return entry.LoggerStdData.Sync()
}

// Debug 方法条目输出Debug级别日志。
func (entry *LoggerStd) Debug(args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < 1 {
		entry.Level = 0
		entry.Message = fmt.Sprintln(args...)
		entry.Message = entry.Message[:len(entry.Message)-1]
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Info 方法条目输出Info级别日志。
func (entry *LoggerStd) Info(args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < 2 {
		entry.Level = 1
		entry.Message = fmt.Sprintln(args...)
		entry.Message = entry.Message[:len(entry.Message)-1]
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Warning 方法条目输出Warning级别日志。
func (entry *LoggerStd) Warning(args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < 3 {
		entry.Level = 2
		entry.Message = fmt.Sprintln(args...)
		entry.Message = entry.Message[:len(entry.Message)-1]
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Error 方法条目输出Error级别日志。
func (entry *LoggerStd) Error(args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < 4 {
		entry.Level = 3
		entry.Message = fmt.Sprintln(args...)
		entry.Message = entry.Message[:len(entry.Message)-1]
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Fatal 方法条目输出Fatal级别日志。
func (entry *LoggerStd) Fatal(args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	entry.Level = 4
	entry.Message = fmt.Sprintln(args...)
	entry.Message = entry.Message[:len(entry.Message)-1]
	entry.LoggerStdData.PutLogger(entry)
}

// Debugf 方法格式化写入流Debug级别日志
func (entry *LoggerStd) Debugf(format string, args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < LoggerInfo {
		entry.Level = LoggerDebug
		entry.Message = fmt.Sprintf(format, args...)
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Infof 方法格式写入流出Info级别日志
func (entry *LoggerStd) Infof(format string, args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < LoggerWarning {
		entry.Level = LoggerInfo
		entry.Message = fmt.Sprintf(format, args...)
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Warningf 方法格式化输出写入流Warning级别日志
func (entry *LoggerStd) Warningf(format string, args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < LoggerError {
		entry.Level = LoggerWarning
		entry.Message = fmt.Sprintf(format, args...)
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Errorf 方法格式化写入流Error级别日志
func (entry *LoggerStd) Errorf(format string, args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	if entry.Level < LoggerFatal {
		entry.Level = LoggerError
		entry.Message = fmt.Sprintf(format, args...)
	} else {
		entry.Keys = entry.Keys[0:0]
		entry.Vals = entry.Vals[0:0]
	}
	entry.LoggerStdData.PutLogger(entry)
}

// Fatalf 方法格式化写入流Fatal级别日志
func (entry *LoggerStd) Fatalf(format string, args ...interface{}) {
	if entry.Logger {
		entry = entry.getEntry()
	}
	entry.Level = 4
	entry.Message = fmt.Sprintf(format, args...)
	entry.LoggerStdData.PutLogger(entry)
}

// WithFields 方法一次设置多个条目属性。
//
// 如果key和val同时为nil会返回Logger的深拷贝对象。
//
// WithFields不会设置Field属性。
func (entry *LoggerStd) WithFields(key []string, value []interface{}) Logger {
	if entry.Logger {
		entry = entry.getEntry()
	}
	entry.Keys = append(entry.Keys, key...)
	entry.Vals = append(entry.Vals, value...)
	return entry
}

// WithField 方法设置一个日志属性。
//
// 如果key为"context"值类型为context.Context,设置该值用于传递自定义信息。
//
// 如果key为"depth"值类型为int，设置日志调用堆栈增删层数。
//
// 如果key为"depth"值类型为string值"enable"或"disable",启用或关闭日志调用位置输出。
//
// 如果key为"time"值类型为time.time，设置日志输出的时间属性。
func (entry *LoggerStd) WithField(key string, value interface{}) Logger {
	if entry.Logger {
		entry = entry.getEntry()
	}
	switch key {
	case "context":
		val, ok := value.(context.Context)
		if ok {
			for i := range entry.Keys {
				if entry.Keys[i] == "context" {
					entry.Vals[i] = val
					return entry
				}
			}
		}
	case "depth":
		return entry.withFieldDepth(key, value)
	case "time":
		val, ok := value.(time.Time)
		if ok {
			entry.Time = val
			return entry
		}
	case "logger":
		val, ok := value.(bool)
		if ok && val {
			entry.Logger = true
			return entry
		}
	}
	entry.Keys = append(entry.Keys, key)
	entry.Vals = append(entry.Vals, value)
	return entry
}

// withFieldDepth 方法处理withDepth属性，cost 67 可内联。
func (entry *LoggerStd) withFieldDepth(key string, value interface{}) Logger {
	val, ok := value.(int)
	if ok {
		entry.Depth += val
		return entry
	}
	vals, ok := value.(string)
	if ok {
		switch vals {
		case "stack":
			entry.Depth |= 0x200
		case "enable":
			entry.Depth |= 0x100
		case "disable":
			entry.Depth &^= 0x300
		}
		return entry
	}
	entry.Keys = append(entry.Keys, key)
	entry.Vals = append(entry.Vals, value)
	return entry
}

var (
	loggerlevels = [][]byte{[]byte("DEBUG"), []byte("INFO"), []byte("WARIRNG"), []byte("ERROR"), []byte("FATAL")}
	loggerpart1  = []byte(`{"time":"`)
	loggerpart2  = []byte(`","level":"`)
	loggerpart3  = []byte(`,"message":"`)
	loggerpart4  = []byte("\"}\n")
	loggerpart5  = []byte("}\n")
	_hex         = "0123456789abcdef"
)

func loggerEntryStdFormat(entry *LoggerStd) {
	t := entry.Time
	if t.IsZero() {
		t = time.Now()
	}
	entry.Buffer = append(entry.Buffer, loggerpart1...)
	entry.Buffer = t.AppendFormat(entry.Buffer, entry.Timeformat)
	entry.Buffer = append(entry.Buffer, loggerpart2...)
	entry.Buffer = append(entry.Buffer, loggerlevels[entry.Level]...)
	entry.Buffer = append(entry.Buffer, '"')

	for i := range entry.Keys {
		entry.Buffer = append(entry.Buffer, ',')
		entry.Buffer = append(entry.Buffer, '"')
		entry.Buffer = append(entry.Buffer, entry.Keys[i]...)
		entry.Buffer = append(entry.Buffer, '"', ':')
		loggerFormatWriteValue(entry, entry.Vals[i])
	}

	if len(entry.Message) > 0 {
		entry.Buffer = append(entry.Buffer, loggerpart3...)
		loggerFormatWriteString(entry, entry.Message)
		entry.Buffer = append(entry.Buffer, loggerpart4...)
	} else {
		entry.Buffer = append(entry.Buffer, loggerpart5...)
	}
}

// String 方法实现ftm.Stringer接口，格式化输出日志级别。
func (l LoggerLevel) String() string {
	return DefaultLoggerLevelString[l]
}

// MarshalText 方法实现encoding.TextMarshaler接口，用于编码日志级别。
func (l LoggerLevel) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

// UnmarshalText 方法实现encoding.TextUnmarshaler接口，用于解码日志级别。
func (l *LoggerLevel) UnmarshalText(text []byte) error {
	str := strings.ToUpper(string(text))
	for i, s := range DefaultLoggerLevelString {
		if s == str {
			*l = LoggerLevel(i)
			return nil
		}
	}
	n, err := strconv.Atoi(str)
	if err == nil && n < 5 && n > -1 {
		*l = LoggerLevel(n)
		return nil
	}
	return ErrLoggerLevelUnmarshalText
}

// logFormatNameFileLine 函数获得调用的文件位置和函数名称。
//
// 文件位置会从第一个src后开始截取，处理gopath下文件位置。
func logFormatNameFileLine(depth int) (string, string, int) {
	ptr, file, line, ok := runtime.Caller(depth)
	if ok {
		slash := strings.Index(file, "src")
		if slash >= 0 {
			file = file[slash+4:]
		}
		return runtime.FuncForPC(ptr).Name(), file, line
	}
	return "", "???", 0
}

// GetPanicStack 函数返回panic栈信息。
func GetPanicStack(depth int) []string {
	pc := make([]uintptr, DefaultLoggerDepth)
	n := runtime.Callers(depth, pc)
	if n == 0 {
		return nil
	}

	stack := make([]string, 0, n)
	frames := runtime.CallersFrames(pc[:n])
	frame, more := frames.Next()
	for more {
		pos := strings.Index(frame.File, "src")
		if pos >= 0 {
			frame.File = frame.File[pos+4:]
		}
		pos = strings.LastIndex(frame.Function, "/")
		if pos >= 0 {
			frame.Function = frame.Function[pos+1:]
		}
		stack = append(stack, fmt.Sprintf("%s:%d %s", frame.File, frame.Line, frame.Function))

		frame, more = frames.Next()
	}
	return stack
}

// WriteValue 方法写入值。
func loggerFormatWriteValue(entry *LoggerStd, value interface{}) {
	iValue := reflect.ValueOf(value)
	loggerFormatWriteReflect(entry, iValue)
}

// loggerFormatWriteReflect 方法写入值。
func loggerFormatWriteReflect(entry *LoggerStd, iValue reflect.Value) {
	if loggerFormatWriteReflectFace(entry, iValue) {
		return
	}
	// 写入类型
	switch iValue.Kind() {
	case reflect.Bool:
		entry.Buffer = strconv.AppendBool(entry.Buffer, iValue.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		entry.Buffer = strconv.AppendInt(entry.Buffer, iValue.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		entry.Buffer = strconv.AppendUint(entry.Buffer, iValue.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		entry.Buffer = strconv.AppendFloat(entry.Buffer, iValue.Float(), 'f', -1, 64)
	case reflect.Complex64, reflect.Complex128:
		val := iValue.Complex()
		r, i := float64(real(val)), float64(imag(val))
		entry.Buffer = append(entry.Buffer, '"')
		entry.Buffer = strconv.AppendFloat(entry.Buffer, r, 'f', -1, 64)
		entry.Buffer = append(entry.Buffer, '+')
		entry.Buffer = strconv.AppendFloat(entry.Buffer, i, 'f', -1, 64)
		entry.Buffer = append(entry.Buffer, 'i')
		entry.Buffer = append(entry.Buffer, '"')
	case reflect.String:
		entry.Buffer = append(entry.Buffer, '"')
		loggerFormatWriteString(entry, iValue.String())
		entry.Buffer = append(entry.Buffer, '"')
	case reflect.Ptr, reflect.Interface:
		loggerFormatWriteReflect(entry, iValue.Elem())
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		entry.Buffer = append(entry.Buffer, '0', 'x')
		entry.Buffer = strconv.AppendUint(entry.Buffer, uint64(iValue.Pointer()), 16)
	case reflect.Map:
		loggerFormatWriteReflectMap(entry, iValue)
	case reflect.Array, reflect.Slice:
		loggerFormatWriteReflectSlice(entry, iValue)
	case reflect.Struct:
		loggerFormatWriteReflectStruct(entry, iValue)
	}
}

func loggerFormatWriteReflectFace(entry *LoggerStd, iValue reflect.Value) bool {
	switch iValue.Kind() {
	case reflect.Map, reflect.Slice:
		if iValue.IsNil() {
			entry.Buffer = append(entry.Buffer, 'n', 'u', 'l', 'l')
			return true
		}
	case reflect.Ptr, reflect.Func, reflect.Chan:
		if iValue.IsNil() {
			entry.Buffer = append(entry.Buffer, 'n', 'u', 'l', 'l')
			return true
		}
	case reflect.Interface:
		return false
	case reflect.Invalid:
		entry.Buffer = append(entry.Buffer, '"', '<', 'I', 'n', 'v', 'a', 'l', 'i', 'd',
			' ', 'V', 'a', 'l', 'u', 'e', '>', '"')
		return true
	}
	// 检查接口
	switch val := iValue.Interface().(type) {
	case json.Marshaler:
		body, err := val.MarshalJSON()
		if err == nil {
			entry.Buffer = append(entry.Buffer, body...)
		} else {
			entry.Buffer = append(entry.Buffer, '"')
			loggerFormatWriteString(entry, err.Error())
			entry.Buffer = append(entry.Buffer, '"')
		}
	case encoding.TextMarshaler:
		body, err := val.MarshalText()
		entry.Buffer = append(entry.Buffer, '"')
		if err == nil {
			loggerFormatWriteBytes(entry, body)
		} else {
			loggerFormatWriteString(entry, err.Error())
		}
		entry.Buffer = append(entry.Buffer, '"')
	case fmt.Stringer:
		entry.Buffer = append(entry.Buffer, '"')
		loggerFormatWriteString(entry, val.String())
		entry.Buffer = append(entry.Buffer, '"')
	case error:
		entry.Buffer = append(entry.Buffer, '"')
		loggerFormatWriteString(entry, val.Error())
		entry.Buffer = append(entry.Buffer, '"')
	default:
		return false
	}
	return true
}

func loggerFormatWriteReflectStruct(entry *LoggerStd, iValue reflect.Value) {
	entry.Buffer = append(entry.Buffer, '{')
	pos := len(entry.Buffer)
	iType := iValue.Type()
	for i := 0; i < iValue.NumField(); i++ {
		if iValue.Field(i).CanInterface() {
			name, omit := split2byte(iType.Field(i).Tag.Get("json"), ',')
			if name == "-" || (omit == "omitempty" && iValue.Field(i).IsZero()) {
				continue
			}
			if name == "" {
				name = iType.Field(i).Name
			}
			entry.Buffer = append(entry.Buffer, '"')
			loggerFormatWriteString(entry, name)
			entry.Buffer = append(entry.Buffer, '"', ':')
			loggerFormatWriteReflect(entry, iValue.Field(i))
			entry.Buffer = append(entry.Buffer, ',')
		}
	}
	if pos == len(entry.Buffer) {
		entry.Buffer = append(entry.Buffer, '}')
	} else {
		entry.Buffer[len(entry.Buffer)-1] = '}'
	}
}

func loggerFormatWriteReflectSlice(entry *LoggerStd, iValue reflect.Value) {
	if iValue.Len() == 0 {
		entry.Buffer = append(entry.Buffer, '[', ']')
		return
	}
	entry.Buffer = append(entry.Buffer, '[')
	for i := 0; i < iValue.Len(); i++ {
		loggerFormatWriteReflect(entry, iValue.Index(i))
		entry.Buffer = append(entry.Buffer, ',')
	}
	entry.Buffer[len(entry.Buffer)-1] = ']'
}

func loggerFormatWriteReflectMap(entry *LoggerStd, iValue reflect.Value) {
	if iValue.Len() == 0 {
		entry.Buffer = append(entry.Buffer, '{', '}')
		return
	}

	entry.Buffer = append(entry.Buffer, '{')
	for _, key := range iValue.MapKeys() {
		loggerFormatWriteReflect(entry, key)
		entry.Buffer = append(entry.Buffer, ':')
		loggerFormatWriteReflect(entry, iValue.MapIndex(key))
		entry.Buffer = append(entry.Buffer, ',')
	}
	entry.Buffer[len(entry.Buffer)-1] = '}'
}

// loggerFormatWriteString 方法安全写入字符串。
func loggerFormatWriteString(entry *LoggerStd, s string) {
	for i := 0; i < len(s); {
		if tryAddRuneSelf(entry, s[i]) {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if tryAddRuneError(entry, r, size) {
			i++
			continue
		}
		entry.Buffer = append(entry.Buffer, s[i:i+size]...)
		i += size
	}
}

// loggerFormatWriteBytes 方法安全写入[]byte的字符串数据。
func loggerFormatWriteBytes(entry *LoggerStd, s []byte) {
	for i := 0; i < len(s); {
		if tryAddRuneSelf(entry, s[i]) {
			i++
			continue
		}
		r, size := utf8.DecodeRune(s[i:])
		if tryAddRuneError(entry, r, size) {
			i++
			continue
		}
		entry.Buffer = append(entry.Buffer, s[i:i+size]...)
		i += size
	}
}

// tryAddRuneSelf appends b if it is valid UTF-8 character represented in a single byte.
func tryAddRuneSelf(entry *LoggerStd, b byte) bool {
	if b >= utf8.RuneSelf {
		return false
	}
	if 0x20 <= b && b != '\\' && b != '"' {
		entry.Buffer = append(entry.Buffer, b)
		return true
	}
	switch b {
	case '\\', '"':
		entry.Buffer = append(entry.Buffer, '\\')
		entry.Buffer = append(entry.Buffer, b)
	case '\n':
		entry.Buffer = append(entry.Buffer, '\\')
		entry.Buffer = append(entry.Buffer, 'n')
	case '\r':
		entry.Buffer = append(entry.Buffer, '\\')
		entry.Buffer = append(entry.Buffer, 'r')
	case '\t':
		entry.Buffer = append(entry.Buffer, '\\')
		entry.Buffer = append(entry.Buffer, 't')
	default:
		// Encode bytes < 0x20, except for the escape sequences above.
		entry.Buffer = append(entry.Buffer, `\u00`...)
		entry.Buffer = append(entry.Buffer, _hex[b>>4])
		entry.Buffer = append(entry.Buffer, _hex[b&0xF])
	}
	return true
}

func tryAddRuneError(entry *LoggerStd, r rune, size int) bool {
	if r == utf8.RuneError && size == 1 {
		entry.Buffer = append(entry.Buffer, `\ufffd`...)
		return true
	}
	return false
}

// LoggerWriter 定义日志写入流，用于写入日志数据。
type LoggerWriter interface {
	Sync() error
	io.Writer
}

type syncWriterFile struct {
	*bufio.Writer
	file *os.File
}

type syncWriterRotate struct {
	name      string
	std       bool
	MaxSize   uint64
	nextindex int
	nexttime  time.Time
	nbytes    uint64
	*bufio.Writer
	file  *os.File
	newfn []func(string)
}

// NewLoggerWriterStd 函数返回一个标准输出流的日志写入流。
func NewLoggerWriterStd() LoggerWriter {
	return os.Stdout
}

// NewLoggerWriterFile 函数创建一个文件输出的日志写入流。
func NewLoggerWriterFile(name string, std bool) (LoggerWriter, error) {
	if name == "" {
		return NewLoggerWriterStd(), nil
	}
	os.MkdirAll(filepath.Dir(name), 0644)
	file, err := os.OpenFile(formatDateName(name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil, err
	}

	if std {
		return &syncWriterFile{bufio.NewWriter(io.MultiWriter(os.Stdout, file)), file}, nil
	}
	return &syncWriterFile{bufio.NewWriter(file), file}, nil
}

// Sync 方法将缓冲数据写入到文件。
func (w syncWriterFile) Sync() error {
	w.Flush()
	return w.file.Sync()
}

// NewLoggerWriterRotate 函数创建一个支持文件切割的的日志写入流。
func NewLoggerWriterRotate(name string, std bool, maxsize uint64, fn ...func(string)) (LoggerWriter, error) {
	if strings.Index(name, "index") == -1 {
		maxsize = 0
	}
	if maxsize <= 0 {
		// 如果同时文件名称不包含日期，那么就具有index和date日志滚动条件。
		if name == formatDateName(name) {
			return NewLoggerWriterFile(name, std)
		}
		maxsize = 0xffffffffff
	}
	lw := &syncWriterRotate{
		name:     name,
		std:      std,
		MaxSize:  maxsize,
		nexttime: getNextHour(),
		newfn:    fn,
	}
	return lw, lw.rotateFile()
}

// Sync 方法将缓冲数据写入到文件。
func (w *syncWriterRotate) Sync() error {
	if w.file == nil {
		return nil
	}
	w.Flush()
	return w.file.Sync()
}

// Write 方法写入日志数据。
func (w *syncWriterRotate) Write(p []byte) (n int, err error) {
	if w.nbytes+uint64(len(p)) >= w.MaxSize {
		// 执行size滚动
		w.rotateFile()
	}
	if time.Now().After(w.nexttime) {
		w.nexttime = getNextHour()
		// 检查时间变化
		if strings.Replace(formatDateName(w.name), "index", fmt.Sprint(w.nextindex-1), -1) != w.file.Name() {
			w.nextindex = 0
			w.rotateFile()
		}
	}
	n, err = w.Writer.Write(p)
	if w.std {
		os.Stdout.Write(p)
	}
	w.nbytes += uint64(n)
	return
}

func (w *syncWriterRotate) rotateFile() error {
	name := formatDateName(w.name)
	for {
		name := strings.Replace(name, "index", fmt.Sprint(w.nextindex), -1)
		os.MkdirAll(filepath.Dir(name), 0644)
		file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return err
		}
		w.nextindex++
		// 检查open新文件size小于MaxSize
		stat, _ := file.Stat()
		w.nbytes = uint64(stat.Size())
		if w.nbytes < w.MaxSize {
			w.Sync()
			w.file.Close()
			w.Writer = bufio.NewWriter(file)
			w.file = file
			for _, fn := range w.newfn {
				fn(name)
			}
			return nil
		}
		file.Close()
	}
}

func formatDateName(name string) string {
	now := time.Now()
	name = strings.Replace(name, "yyyy", "2006", 1)
	name = strings.Replace(name, "yy", "06", 1)
	name = strings.Replace(name, "MM", "01", 1)
	name = strings.Replace(name, "dd", "02", 1)
	name = strings.Replace(name, "HH", "15", 1)
	return now.Format(name)
}

func getNextHour() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, now.Location())
}

func newLoggerLinkName(link string) func(string) {
	os.MkdirAll(filepath.Dir(link), 0644)
	return func(name string) {
		if link == "" {
			return
		}
		if name[0] != '/' {
			pwd, _ := os.Getwd()
			name = filepath.Join(pwd, name)
		}
		os.Remove(link)
		os.Symlink(name, link)
	}
}

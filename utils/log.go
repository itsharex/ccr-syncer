package utils

import (
	"flag"
	"fmt"
	"io"
	"os"

	filename "github.com/keepeye/logrus-filename"
	log "github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
	prefixed "github.com/t-tomalak/logrus-prefixed-formatter"
)

var (
	logLevel        string
	logFilename     string
	logAlsoToStderr bool
)

func init() {
	flag.StringVar(&logLevel, "log_level", "trace", "log level")
	flag.StringVar(&logFilename, "log_filename", "", "log filename")
	flag.BoolVar(&logAlsoToStderr, "log_also_to_stderr", false, "log also to stderr")
}

func InitLog() {
	level, err := log.ParseLevel(logLevel)
	if err != nil {
		fmt.Printf("parse log level %v failed: %v\n", logLevel, err)
		os.Exit(1)
	}
	log.SetLevel(level)
	log.SetFormatter(&prefixed.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceFormatting: true,
	})

	syncHook := NewHook()
	log.AddHook(syncHook)
	if level > log.InfoLevel {
		// log.SetReportCaller(true), caller by filename
		filenameHook := filename.NewHook()
		filenameHook.Field = "line"
		log.AddHook(filenameHook)
	}
	
	// TODO: Add write permission check
	if logFilename != "" {
		output := &lumberjack.Logger{
			Filename:   logFilename,
			MaxSize:    100,
			MaxAge:     7,
			MaxBackups: 30,
			LocalTime:  true,
			Compress:   false,
		}
		if logAlsoToStderr {
			writer := io.MultiWriter(output, os.Stderr)
			log.SetOutput(writer)
		} else {
			log.SetOutput(output)
		}
	} else {
		log.SetOutput(os.Stdout)
	}

	
}

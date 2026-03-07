package logx

import (
	"log"
	"os"
)

func New() *log.Logger {
	return log.New(os.Stdout, "hubfly-tool-manager ", log.LstdFlags|log.Lmicroseconds|log.LUTC)
}

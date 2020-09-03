package logging

import "go.uber.org/zap"

type ImapAdapter struct {
	Logger *zap.SugaredLogger
}

func (i ImapAdapter) Printf(format string, v ...interface{}) {
	i.Logger.Errorf(format, v)
}

func (i ImapAdapter) Println(v ...interface{}) {
	i.Logger.Error(v)
}

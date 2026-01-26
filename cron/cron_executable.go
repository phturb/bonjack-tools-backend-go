package cron

type CronExecutable interface {
	Run()
	JobSpec() string
}

package discord

import (
	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
)

type discordManager struct {
	session *discordgo.Session
}

type DiscordManager interface {
	Session() *discordgo.Session
	GetConfigChannel() (*discordgo.Channel, error)
}

var _ DiscordManager = (*discordManager)(nil)

func NewDiscordManager() (*discordManager, error) {
	ds, err := discordgo.New("Bot " + config.Config().Discord.Token)
	if err != nil {
		return nil, err
	}

	ds.Identify.Intents = discordgo.MakeIntent(ds.Identify.Intents | discordgo.IntentsGuildMessages | discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuildMembers | discordgo.IntentsGuildPresences)

	ds.StateEnabled = true

	d := &discordManager{
		session: ds,
	}

	d.session.AddHandler(d.onReady)
	return d, nil
}

func (d *discordManager) onReady(s *discordgo.Session, e *discordgo.Ready) {
	l := logger.ComponentLogger("onReady")
	l.Info("discord bot started", "username", e.User.Username)
	s.StateEnabled = true
	s.State.TrackChannels = true
	s.State.TrackThreadMembers = true
	s.State.TrackVoice = true
	s.State.TrackThreads = true
	s.State.TrackMembers = true
}

func (d *discordManager) Session() *discordgo.Session {
	return d.session
}

func (d *discordManager) GetConfigChannel() (*discordgo.Channel, error) {
	l := logger.ComponentLogger("GetConfigChannel")
	ch, err := d.session.Channel(config.Config().Discord.ChannelID)
	if err != nil {
		l.WithError(err).Error("failed to get discord config channel")
		return nil, err
	}
	for _, c := range d.session.State.Guilds[0].Channels {
		l.Info("discord channel info", "channel_name", c.Name, "members_count", len(c.Members))
	}
	return ch, nil
}

package discord

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal"
)

type discordManager struct {
	session *discordgo.Session

	vsMu sync.RWMutex
	vs   *discordgo.VoiceState
}

type DiscordManager interface {
	Session() *discordgo.Session
	GetConfigChannel() (*discordgo.Channel, error)
}

var _ DiscordManager = (*discordManager)(nil)

func NewDiscordManager() (*discordManager, error) {
	ds, err := discordgo.New("Bot " + internal.Config().Discord.Token)
	if err != nil {
		return nil, err
	}

	ds.Identify.Intents = discordgo.IntentsAll
	// ds.Identify.Intents = discordgo.MakeIntent(ds.Identify.Intents | discordgo.IntentsGuildMessages | discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuildMembers | discordgo.IntentsGuildPresences)

	ds.StateEnabled = true

	d := &discordManager{
		session: ds,
	}

	d.session.AddHandler(d.onReady)
	return d, nil
}

func (d *discordManager) onReady(s *discordgo.Session, e *discordgo.Ready) {
	slog.Info("discord bot started as '" + e.User.Username + "'")
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
	ch, err := d.session.Channel(internal.Config().Discord.ChannelID)
	if err != nil {
		slog.Error(err.Error())
		return nil, err
	}
	for _, c := range d.session.State.Guilds[0].Channels {
		slog.Info(fmt.Sprintf("%s : %+v", c.Name, c.Members))
	}
	return ch, nil
}

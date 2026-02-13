package discord

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Arno500/go-plex-client"
	discordRP "github.com/brw/rp"
	rpc "github.com/brw/rp/rpc"
	i18npkg "github.com/nicksnyder/go-i18n/v2/i18n"
	"gitlab.com/Arno500/plex-richpresence/i18n"
	"gitlab.com/Arno500/plex-richpresence/types"
)

var currentPlayState types.PlayState
var discord *rpc.Client
var client = &http.Client{
	Timeout: 5 * time.Second,
}

const EMPTY_THUMB_STRING = "plex"
const DISCORD_PLEX_CLIENT_ID = "803556010307616788"
const DISCORD_CRUNCHYROLL_CLIENT_ID = "981509069309354054"

// prepare Discord's RPC API to allow Rich Presence
func InitPlexDiscordClient() {
	if discord != nil && discord.ClientID == DISCORD_PLEX_CLIENT_ID {
		discord.Login()
		return
	}
	LogoutDiscordClient()

	discordInstance, err := discordRP.NewClient(DISCORD_PLEX_CLIENT_ID)
	if err != nil {
		log.Println(err)
		return
	}

	discord = discordInstance
}

// use Crunchyroll's app ID instead so Discord uses 3/2 thumbnails instead of cropping it to a square
func InitCrunchyrollDiscordClient() {
	if discord != nil && discord.ClientID == DISCORD_CRUNCHYROLL_CLIENT_ID {
		discord.Login()
		return
	}
	LogoutDiscordClient()

	discordInstance, err := discordRP.NewClient(DISCORD_CRUNCHYROLL_CLIENT_ID)
	if err != nil {
		log.Println(err)
		return
	}

	discord = discordInstance
}

// LogoutDiscordClient logout from Discord
func LogoutDiscordClient() {
	if discord != nil && discord.Logged {
		discord.Logout()
	}
}

func getThumbnailLink(thumbKey string, plexInstance *plex.Plex) string {
	if currentPlayState.Thumb.PlexThumbUrl == thumbKey {
		if currentPlayState.Thumb.ImgLink != EMPTY_THUMB_STRING {
			return currentPlayState.Thumb.ImgLink
		}
	}

	currentPlayState.Thumb.PlexThumbUrl = thumbKey
	plexThumbLink := fmt.Sprintf("%s/photo/:/transcode?width=450&height=253&minSize=1&upscale=1&X-Plex-Token=%s&url=%s", plexInstance.URL, plexInstance.Token, thumbKey)

	thumbResp, err := client.Get(plexThumbLink)
	if err != nil {
		log.Printf("Couldn't get thumbnail from Plex (%s)", err)
		currentPlayState.Thumb.ImgLink = EMPTY_THUMB_STRING
		return EMPTY_THUMB_STRING
	}
	defer thumbResp.Body.Close()

	b, err := io.ReadAll(thumbResp.Body)
	if err != nil {
		log.Printf("Couldn't read thumbnail from Plex (%s)", err)
		currentPlayState.Thumb.ImgLink = EMPTY_THUMB_STRING
		return EMPTY_THUMB_STRING
	}

	imgUrl, err := UploadImage(b, thumbKey)
	if err != nil {
		log.Println("Error uploading image to litterbox: ", err)
		currentPlayState.Thumb.ImgLink = EMPTY_THUMB_STRING
		return EMPTY_THUMB_STRING
	}

	currentPlayState.Thumb.ImgLink = imgUrl
	return imgUrl
}

func getPlexDiscoverLink(mediaGuid string) string {
	return fmt.Sprintf("https://app.plex.tv/desktop/#!/provider/tv.plex.provider.discover/details?key=/library/metadata/%s", mediaGuid)
}

// SetRichPresence allows to send Rich Presence informations to Plex from a session info
func SetRichPresence(session types.PlexStableSession) {
	now := time.Now()
	currentPlayState.Alteration.Item = false
	currentPlayState.Alteration.Time = false
	activityInfos := rpc.Activity{
		Name:              "Plex",
		LargeImage:        "plex",
		LargeText:         "Plex",
		StatusDisplayType: rpc.StatusDisplayTypeDetails,
	}

	if session.Media.Type == "track" {
		activityInfos.Type = rpc.ActivityTypeListening
	} else {
		activityInfos.Type = rpc.ActivityTypeWatching
	}

	if currentPlayState.PlayingItem == nil || currentPlayState.PlayingItem.Media.GUID.String() != session.Media.GUID.String() {
		currentPlayState.PlayingItem = &session
		currentPlayState.Alteration.Item = true
	}

	if currentPlayState.PlayState != session.Session.State {
		currentPlayState.PlayState = session.Session.State
		currentPlayState.Alteration.Time = true
	}

	if session.Session.State == "paused" && currentPlayState.Alteration.Time {
		log.Printf("Paused, closing connection to Discord.")
		LogoutDiscordClient()
		return
	} else if (session.Session.State == "playing" || session.Session.State == "buffering") && session.Media.Type != "photo" {
		timeResetThreshold, _ := time.ParseDuration("4s")
		progress, _ := time.ParseDuration(strconv.FormatInt(session.Session.ViewOffset/1000, 10) + "s")
		calculatedStartTime := now.Add(-progress)
		duration, _ := time.ParseDuration(strconv.FormatInt(session.Media.Duration, 10) + "ms")
		calculatedEndTime := calculatedStartTime.Add(duration)
		activityInfos.Timestamps = &rpc.Timestamps{
			Start: &calculatedStartTime,
			End:   &calculatedEndTime,
		}

		if currentPlayState.LastCalculatedTime.Sub(calculatedEndTime).Abs() > timeResetThreshold {
			log.Printf("A seek or a media change was detected, updating state...")
			currentPlayState.Alteration.Time = true
			currentPlayState.LastCalculatedTime = calculatedEndTime
		}
	} else if session.Media.Type == "photo" {
		activityInfos.SmallImage = "camera"
	} else {
		log.Printf("Nothing is playing, closing connection to Discord.")
		LogoutDiscordClient()
		return
	}

	switch session.Media.Type {
	case "episode":
		InitCrunchyrollDiscordClient()

		// Episode title
		activityInfos.State = fmt.Sprintf("%s", session.Media.Title)
		// Show title
		activityInfos.Details = session.Media.GrandparentTitle
		activityInfos.LargeImage = getThumbnailLink(session.Media.GrandparentThumbnail, session.PlexInstance)
		activityInfos.LargeText = fmt.Sprintf("Season %02d, Episode %02d", session.Media.ParentIndex, session.Media.Index)
		activityInfos.DetailsUrl = getPlexDiscoverLink(path.Base(session.Media.GrandparentGUID.EscapedPath()))
		activityInfos.StateUrl = getPlexDiscoverLink(path.Base(session.Media.GUID.EscapedPath()))
		activityInfos.Buttons = append(activityInfos.Buttons, &rpc.Button{
			Label: i18n.Localizer.MustLocalize(&i18npkg.LocalizeConfig{
				DefaultMessage: &i18npkg.Message{
					ID:    "ShowDetails",
					Other: "Show details on Plex",
				},
			}),
			Url: activityInfos.DetailsUrl,
		})
	case "movie":
		InitCrunchyrollDiscordClient()

		var formattedMovieName string
		if session.Media.Year > 0 {
			formattedMovieName = fmt.Sprintf("%s (%d)", session.Media.Title, session.Media.Year)
		} else {
			formattedMovieName = session.Media.Title
		}

		// Movie Director(s)
		if len(session.Media.Director) > 0 {
			directors := make([]string, len(session.Media.Director))
			for i, director := range session.Media.Director {
				directors[i] = director.Tag
			}
			activityInfos.State = strings.Join(directors, ", ")
		} else {
			activityInfos.State = "(⌐■_■)"
		}

		activityInfos.Details = formattedMovieName
		activityInfos.DetailsUrl = getPlexDiscoverLink(path.Base(session.Media.GUID.EscapedPath()))
		activityInfos.LargeImage = getThumbnailLink(session.Media.Thumbnail, session.PlexInstance)
		activityInfos.LargeUrl = activityInfos.DetailsUrl
		activityInfos.Buttons = append(activityInfos.Buttons, &rpc.Button{
			Label: i18n.Localizer.MustLocalize(&i18npkg.LocalizeConfig{
				DefaultMessage: &i18npkg.Message{
					ID:    "MovieDetails",
					Other: "Movie details on Plex",
				},
			}),
			Url: activityInfos.DetailsUrl,
		})
	case "track":
		InitPlexDiscordClient()

		artist := ""
		if session.Media.OriginalTitle != "" {
			artist = session.Media.OriginalTitle
		} else {
			artist = session.Media.GrandparentTitle
		}

		activityInfos.State = artist
		activityInfos.StateUrl = fmt.Sprintf("https://listen.plex.tv/artist/%s", path.Base(session.Media.GrandparentGUID.EscapedPath()))
		activityInfos.Details = session.Media.Title
		activityInfos.DetailsUrl = fmt.Sprintf("https://listen.plex.tv/track/%s?parentGuid=%s&grandparentGuid=%s", path.Base(session.Media.GUID.EscapedPath()), path.Base(session.Media.ParentGUID.EscapedPath()), path.Base(session.Media.GrandparentGUID.EscapedPath()))
		activityInfos.LargeImage = getThumbnailLink(session.Media.ParentThumbnail, session.PlexInstance)
		activityInfos.LargeUrl = activityInfos.DetailsUrl
		activityInfos.Buttons = append(activityInfos.Buttons, &rpc.Button{
			Label: i18n.Localizer.MustLocalize(&i18npkg.LocalizeConfig{
				DefaultMessage: &i18npkg.Message{
					ID:    "TrackDetails",
					Other: "Track details on Plex",
				},
			}),
			Url: activityInfos.DetailsUrl,
		}, &rpc.Button{
			Label: i18n.Localizer.MustLocalize(&i18npkg.LocalizeConfig{
				DefaultMessage: &i18npkg.Message{
					ID:    "YoutubeSearch",
					Other: "Search on YouTube",
				},
			}),
			Url: fmt.Sprintf("https://www.youtube.com/results?search_query=%s", url.QueryEscape(artist+" "+session.Media.Title)),
		})
		activityInfos.LargeText = session.Media.ParentTitle
	case "photo":
		InitPlexDiscordClient()

		text := i18n.Localizer.MustLocalize(&i18npkg.LocalizeConfig{
			DefaultMessage: &i18npkg.Message{
				ID:    "WatchingPhotos",
				Other: "Watching photos",
			},
		})
		activityInfos.State = text
		activityInfos.SmallText = text
		activityInfos.Details = session.Media.Title
	case "clip":
		InitPlexDiscordClient()

		// Trailer data (preroll)
		activityInfos.State = session.Media.Title
		activityInfos.SmallText = "Preroll"
	}

	err := discord.SetActivity(&activityInfos)
	if err != nil {
		log.Printf("An error occured when setting the activity in Discord: %v", err)
		discord = nil
	} else {
		log.Printf("Discord activity set")
	}
}

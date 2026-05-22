package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

var wallets = map[string][]string{}

type BlockcypherAddress struct {
	Balance int64 `json:"balance"`
	NTx     int   `json:"n_tx"`
}

type CoinGeckoPrice struct {
	Litecoin struct {
		USD float64 `json:"usd"`
	} `json:"litecoin"`
}

func getLTCPrice() (float64, error) {
	resp, err := http.Get("https://api.coingecko.com/api/v3/simple/price?ids=litecoin&vs_currencies=usd")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var data CoinGeckoPrice
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}
	return data.Litecoin.USD, nil
}

func getWalletBalance(address string) (float64, int, error) {
	url := fmt.Sprintf("https://api.blockcypher.com/v1/ltc/main/addrs/%s/balance", address)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "ltc-portfolio-bot/1.0")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("read body: %w", err)
	}
	log.Printf("[blockcypher] status=%d body=%s", resp.StatusCode, string(bodyBytes))

	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("blockcypher error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var data BlockcypherAddress
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return 0, 0, fmt.Errorf("json decode: %w", err)
	}
	return float64(data.Balance) / 1e8, data.NTx, nil
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
	})
}

func handleAdd(s *discordgo.Session, i *discordgo.InteractionCreate, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	userID := i.Member.User.ID
	address := opts[0].StringValue()

	if !strings.HasPrefix(address, "L") && !strings.HasPrefix(address, "M") && !strings.HasPrefix(address, "ltc1") {
		respond(s, i, "❌ Invalid LTC address. Should start with `L`, `M`, or `ltc1`.")
		return
	}
	for _, w := range wallets[userID] {
		if w == address {
			respond(s, i, "⚠️ Wallet already linked.")
			return
		}
	}
	if len(wallets[userID]) >= 5 {
		respond(s, i, "❌ Max 5 wallets. Remove one first with `/wallet remove`.")
		return
	}
	wallets[userID] = append(wallets[userID], address)
	respond(s, i, fmt.Sprintf("✅ Wallet linked!\n```\n%s\n```", address))
}

func handleRemove(s *discordgo.Session, i *discordgo.InteractionCreate, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	userID := i.Member.User.ID
	address := opts[0].StringValue()
	list := wallets[userID]
	for idx, w := range list {
		if w == address {
			wallets[userID] = append(list[:idx], list[idx+1:]...)
			respond(s, i, fmt.Sprintf("🗑️ Removed:\n```\n%s\n```", address))
			return
		}
	}
	respond(s, i, "❌ Wallet not found in your list.")
}

func handleList(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := i.Member.User.ID
	list := wallets[userID]
	if len(list) == 0 {
		respond(s, i, "📭 No wallets linked. Use `/wallet add <address>`.")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Your linked wallets (%d/5)**\n\n", len(list)))
	for idx, w := range list {
		sb.WriteString(fmt.Sprintf("`%d.` `%s`\n", idx+1, w))
	}
	respond(s, i, sb.String())
}

func handlePortfolio(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := i.Member.User.ID
	list := wallets[userID]
	if len(list) == 0 {
		respond(s, i, "📭 No wallets linked. Use `/wallet add <address>`.")
		return
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})

	ltcPrice, err := getLTCPrice()
	if err != nil {
		s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Failed to fetch LTC price: `%v`", err),
		})
		return
	}

	var totalLTC float64
	var fields []*discordgo.MessageEmbedField

	for _, addr := range list {
		short := addr[:6] + "..." + addr[len(addr)-4:]
		balance, txCount, err := getWalletBalance(addr)
		if err != nil {
			log.Printf("[error] %s: %v", addr, err)
			fields = append(fields, &discordgo.MessageEmbedField{
				Name:  fmt.Sprintf("🔴 `%s`", short),
				Value: fmt.Sprintf("`%v`", err),
			})
			continue
		}
		totalLTC += balance
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("🔵 `%s`", short),
			Value: fmt.Sprintf("**%.4f LTC** — $%.2f\n`%d` transactions", balance, balance*ltcPrice, txCount),
		})
	}

	s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{{
			Title:       "🪙 Your LTC Portfolio",
			Description: fmt.Sprintf("**Total: %.4f LTC** ≈ $%.2f USD\n*LTC price: $%.2f*", totalLTC, totalLTC*ltcPrice, ltcPrice),
			Color:       0xBFBFBF,
			Fields:      fields,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Powered by BlockCypher & CoinGecko • Read-only, your keys are safe"},
		}},
	})
}

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        "wallet",
		Description: "Manage your linked LTC wallets",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Name: "add", Description: "Link a Litecoin wallet (read-only)",
				Type: discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{Name: "address", Description: "Your LTC address", Type: discordgo.ApplicationCommandOptionString, Required: true},
				},
			},
			{
				Name: "remove", Description: "Unlink a wallet",
				Type: discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{Name: "address", Description: "Address to remove", Type: discordgo.ApplicationCommandOptionString, Required: true},
				},
			},
			{Name: "list", Description: "Show linked wallets", Type: discordgo.ApplicationCommandOptionSubCommand},
		},
	},
	{Name: "portfolio", Description: "View live LTC balances and USD value"},
}

func main() {
	godotenv.Load()

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN not set")
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal(err)
	}

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("✅ Logged in as %s", s.State.User.Username)
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		data := i.ApplicationCommandData()
		switch data.Name {
		case "portfolio":
			handlePortfolio(s, i)
		case "wallet":
			if len(data.Options) == 0 {
				return
			}
			sub := data.Options[0]
			switch sub.Name {
			case "add":
				handleAdd(s, i, sub.Options)
			case "remove":
				handleRemove(s, i, sub.Options)
			case "list":
				handleList(s, i)
			}
		}
	})

	if err := dg.Open(); err != nil {
		log.Fatal(err)
	}
	defer dg.Close()

	guildID := os.Getenv("GUILD_ID")
	for _, cmd := range commands {
		if _, err := dg.ApplicationCommandCreate(dg.State.User.ID, guildID, cmd); err != nil {
			log.Printf("Cannot create command %q: %v", cmd.Name, err)
		}
	}

	log.Println("🤖 Bot running. Ctrl+C to stop.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
}

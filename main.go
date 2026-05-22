package main

import (
	"database/sql"
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
	_ "modernc.org/sqlite"
)

// ── Database ──────────────────────────────────────────────────────────────────

var db *sql.DB

func initDB() {
	var err error
	db, err = sql.Open("sqlite", "wallets.db")
	if err != nil {
		log.Fatal("failed to open db:", err)
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS wallets (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id   TEXT NOT NULL,
			address   TEXT NOT NULL,
			coin      TEXT NOT NULL,
			UNIQUE(user_id, address)
		)
	`)
	if err != nil {
		log.Fatal("failed to create table:", err)
	}
	log.Println("✅ Database ready")
}

func dbAddWallet(userID, address, coin string) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO wallets (user_id, address, coin) VALUES (?, ?, ?)`,
		userID, address, coin,
	)
	return err
}

func dbRemoveWallet(userID, address string) (bool, error) {
	res, err := db.Exec(`DELETE FROM wallets WHERE user_id = ? AND address = ?`, userID, address)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbListWallets(userID string) ([]walletRow, error) {
	rows, err := db.Query(`SELECT address, coin FROM wallets WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []walletRow
	for rows.Next() {
		var w walletRow
		if err := rows.Scan(&w.Address, &w.Coin); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

func dbCountWallets(userID string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM wallets WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

func dbWalletExists(userID, address string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM wallets WHERE user_id = ? AND address = ?`, userID, address).Scan(&n)
	return n > 0, err
}

type walletRow struct {
	Address string
	Coin    string
}

// ── Coin config ───────────────────────────────────────────────────────────────

type coinConfig struct {
	Name       string
	Symbol     string
	Emoji      string
	Prefixes   []string // valid address prefixes
	GeckoID    string   // coingecko price id
	CyperChain string   // blockcypher chain slug (empty = use other api)
	Decimals   float64  // satoshi divisor
}

var coins = map[string]coinConfig{
	"LTC": {
		Name: "Litecoin", Symbol: "LTC", Emoji: "🪙",
		Prefixes:   []string{"L", "M", "ltc1"},
		GeckoID:    "litecoin",
		CyperChain: "ltc/main",
		Decimals:   1e8,
	},
	"BTC": {
		Name: "Bitcoin", Symbol: "BTC", Emoji: "₿",
		Prefixes:   []string{"1", "3", "bc1"},
		GeckoID:    "bitcoin",
		CyperChain: "btc/main",
		Decimals:   1e8,
	},
	"ETH": {
		Name: "Ethereum", Symbol: "ETH", Emoji: "💎",
		Prefixes: []string{"0x"},
		GeckoID:  "ethereum",
		Decimals: 1e18,
	},
	"SOL": {
		Name: "Solana", Symbol: "SOL", Emoji: "◎",
		Prefixes: []string{}, // base58, no fixed prefix
		GeckoID:  "solana",
		Decimals: 1e9,
	},
}

func detectCoin(address string) (string, bool) {
	// ETH
	if strings.HasPrefix(address, "0x") && len(address) == 42 {
		return "ETH", true
	}
	// BTC
	for _, p := range []string{"1", "3", "bc1"} {
		if strings.HasPrefix(address, p) {
			return "BTC", true
		}
	}
	// LTC
	for _, p := range []string{"L", "M", "ltc1"} {
		if strings.HasPrefix(address, p) {
			return "LTC", true
		}
	}
	// SOL: base58, 32-44 chars, no 0/O/I/l
	if len(address) >= 32 && len(address) <= 44 {
		valid := true
		for _, c := range address {
			if c == '0' || c == 'O' || c == 'I' || c == 'l' {
				valid = false
				break
			}
		}
		if valid {
			return "SOL", true
		}
	}
	return "", false
}

// ── Price fetching ────────────────────────────────────────────────────────────

func getPrices(geckoIDs []string) (map[string]float64, error) {
	ids := strings.Join(geckoIDs, ",")
	url := fmt.Sprintf("https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=usd", ids)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for id, v := range raw {
		out[id] = v["usd"]
	}
	return out, nil
}

// ── Balance fetching ──────────────────────────────────────────────────────────

type BlockcypherResp struct {
	Balance int64 `json:"balance"`
	NTx     int   `json:"n_tx"`
}

func getBalanceBlockcypher(address, chain string) (float64, int, error) {
	url := fmt.Sprintf("https://api.blockcypher.com/v1/%s/addrs/%s/balance", chain, address)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "ltc-portfolio-bot/1.0")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[blockcypher/%s] status=%d", chain, resp.StatusCode)
	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("blockcypher %d: %s", resp.StatusCode, string(body))
	}
	var data BlockcypherResp
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, 0, err
	}
	return float64(data.Balance) / 1e8, data.NTx, nil
}

type EtherscanResp struct {
	Status string `json:"status"`
	Result string `json:"result"`
}

func getBalanceETH(address string) (float64, int, error) {
	apiKey := os.Getenv("ETHERSCAN_API_KEY") // optional, works without it (slower)
	url := fmt.Sprintf("https://api.etherscan.io/api?module=account&action=balance&address=%s&tag=latest&apikey=%s", address, apiKey)
	resp, err := http.Get(url)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var data EtherscanResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, err
	}
	if data.Status != "1" {
		return 0, 0, fmt.Errorf("etherscan error: %s", data.Result)
	}
	var wei float64
	fmt.Sscanf(data.Result, "%f", &wei)
	return wei / 1e18, 0, nil
}

type SolanaResp struct {
	Result struct {
		Value struct {
			Lamports int64 `json:"lamports"`
		} `json:"value"`
	} `json:"result"`
}

func getBalanceSOL(address string) (float64, int, error) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"getBalance","params":["%s"]}`, address)
	resp, err := http.Post("https://api.mainnet-beta.solana.com", "application/json", strings.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var data SolanaResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, err
	}
	return float64(data.Result.Value.Lamports) / 1e9, 0, nil
}

func getBalance(address, symbol string) (float64, int, error) {
	cfg, ok := coins[symbol]
	if !ok {
		return 0, 0, fmt.Errorf("unknown coin %s", symbol)
	}
	switch symbol {
	case "ETH":
		return getBalanceETH(address)
	case "SOL":
		return getBalanceSOL(address)
	default:
		return getBalanceBlockcypher(address, cfg.CyperChain)
	}
}

// ── Discord helpers ───────────────────────────────────────────────────────────

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
	})
}

func userID(i *discordgo.InteractionCreate) string {
	if i.Member != nil {
		return i.Member.User.ID
	}
	return i.User.ID
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func handleAdd(s *discordgo.Session, i *discordgo.InteractionCreate, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	uid := userID(i)
	address := strings.TrimSpace(opts[0].StringValue())

	coin, ok := detectCoin(address)
	if !ok {
		respond(s, i, "❌ Could not detect coin from address. Supported: **BTC**, **ETH**, **LTC**, **SOL**.")
		return
	}

	exists, _ := dbWalletExists(uid, address)
	if exists {
		respond(s, i, "⚠️ That wallet is already linked.")
		return
	}

	count, _ := dbCountWallets(uid)
	if count >= 10 {
		respond(s, i, "❌ Max **10 wallets** per user. Remove one first with `/wallet remove`.")
		return
	}

	if err := dbAddWallet(uid, address, coin); err != nil {
		respond(s, i, fmt.Sprintf("❌ Database error: `%v`", err))
		return
	}

	cfg := coins[coin]
	respond(s, i, fmt.Sprintf("%s **%s wallet linked!**\n```\n%s\n```", cfg.Emoji, cfg.Name, address))
}

func handleRemove(s *discordgo.Session, i *discordgo.InteractionCreate, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	uid := userID(i)
	address := strings.TrimSpace(opts[0].StringValue())

	removed, err := dbRemoveWallet(uid, address)
	if err != nil {
		respond(s, i, fmt.Sprintf("❌ Database error: `%v`", err))
		return
	}
	if !removed {
		respond(s, i, "❌ Wallet not found in your list.")
		return
	}
	respond(s, i, fmt.Sprintf("🗑️ Removed:\n```\n%s\n```", address))
}

func handleList(s *discordgo.Session, i *discordgo.InteractionCreate) {
	uid := userID(i)
	list, err := dbListWallets(uid)
	if err != nil || len(list) == 0 {
		respond(s, i, "📭 No wallets linked. Use `/wallet add <address>`.")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Your linked wallets (%d/10)**\n\n", len(list)))
	for idx, w := range list {
		cfg := coins[w.Coin]
		sb.WriteString(fmt.Sprintf("`%d.` %s `%s` — `%s`\n", idx+1, cfg.Emoji, w.Coin, w.Address))
	}
	respond(s, i, sb.String())
}

func handlePortfolio(s *discordgo.Session, i *discordgo.InteractionCreate) {
	uid := userID(i)
	list, err := dbListWallets(uid)
	if err != nil || len(list) == 0 {
		respond(s, i, "📭 No wallets linked. Use `/wallet add <address>`.")
		return
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})

	// Collect unique gecko IDs needed
	geckoIDs := []string{}
	seen := map[string]bool{}
	for _, w := range list {
		id := coins[w.Coin].GeckoID
		if !seen[id] {
			geckoIDs = append(geckoIDs, id)
			seen[id] = true
		}
	}

	prices, err := getPrices(geckoIDs)
	if err != nil {
		s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Failed to fetch prices: `%v`", err),
		})
		return
	}

	// totalUSD across all coins
	var totalUSD float64
	// per-coin totals
	coinTotals := map[string]float64{}
	var fields []*discordgo.MessageEmbedField

	for _, w := range list {
		cfg := coins[w.Coin]
		short := w.Address
		if len(short) > 10 {
			short = short[:6] + "..." + short[len(short)-4:]
		}

		balance, txCount, err := getBalance(w.Address, w.Coin)
		if err != nil {
			log.Printf("[error] %s %s: %v", w.Coin, w.Address, err)
			fields = append(fields, &discordgo.MessageEmbedField{
				Name:  fmt.Sprintf("%s `%s` `%s`", cfg.Emoji, w.Coin, short),
				Value: fmt.Sprintf("❌ `%v`", err),
			})
			continue
		}

		price := prices[cfg.GeckoID]
		usd := balance * price
		totalUSD += usd
		coinTotals[w.Coin] += balance

		txStr := ""
		if txCount > 0 {
			txStr = fmt.Sprintf("\n`%d` transactions", txCount)
		}

		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("%s `%s` `%s`", cfg.Emoji, w.Coin, short),
			Value: fmt.Sprintf("**%.6f %s** — $%.2f%s", balance, w.Coin, usd, txStr),
		})
	}

	// Summary line per coin
	var summaryParts []string
	for _, sym := range []string{"BTC", "ETH", "LTC", "SOL"} {
		if total, ok := coinTotals[sym]; ok && total > 0 {
			cfg := coins[sym]
			summaryParts = append(summaryParts, fmt.Sprintf("%s %.6f %s", cfg.Emoji, total, sym))
		}
	}

	desc := strings.Join(summaryParts, "   ") + fmt.Sprintf("\n\n**Total ≈ $%.2f USD**", totalUSD)

	s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{{
			Title:       "💼 Your Crypto Portfolio",
			Description: desc,
			Color:       0x5865F2,
			Fields:      fields,
			Footer:      &discordgo.MessageEmbedFooter{Text: "BlockCypher • Etherscan • Solana RPC • CoinGecko — Read-only"},
		}},
	})
}

// ── Commands ──────────────────────────────────────────────────────────────────

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        "wallet",
		Description: "Manage your crypto wallets",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Name: "add", Description: "Link a wallet — coin is auto-detected (BTC/ETH/LTC/SOL)",
				Type: discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{Name: "address", Description: "Your wallet address", Type: discordgo.ApplicationCommandOptionString, Required: true},
				},
			},
			{
				Name: "remove", Description: "Unlink a wallet",
				Type: discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{Name: "address", Description: "Address to remove", Type: discordgo.ApplicationCommandOptionString, Required: true},
				},
			},
			{Name: "list", Description: "Show all your linked wallets", Type: discordgo.ApplicationCommandOptionSubCommand},
		},
	},
	{Name: "portfolio", Description: "Live balances and USD value across all your wallets"},
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	godotenv.Load()

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN not set")
	}

	initDB()

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
			log.Printf("Cannot create %q: %v", cmd.Name, err)
		}
	}

	log.Println("🤖 Bot running. Ctrl+C to stop.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("Shutting down...")
}

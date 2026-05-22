<div align="center">

# 🪙 LTC Portfolio Bot

**A Discord bot to track your Litecoin wallets — live balances, USD value, transaction count.**

[![Go Version](https://img.shields.io/badge/Go-1.21+-00acd7?style=for-the-badge&logo=go&logoColor=white)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-silver?style=for-the-badge)](LICENSE)
[![Litecoin](https://img.shields.io/badge/Litecoin-LTC-a6a9aa?style=for-the-badge&logo=litecoin&logoColor=white)](https://litecoin.org)
[![No Key Needed](https://img.shields.io/badge/API%20Key-Not%20Required-3ba55c?style=for-the-badge)]()

</div>

---

## 📸 Preview

> `/portfolio` — shows all your linked wallets with live LTC balance and USD value

```
🪙 Your LTC Portfolio
Total: 2.5000 LTC ≈ $135.50 USD
LTC price: $54.20

🔵 LLLhLe...yYaC
2.5000 LTC — $135.50
142 transactions

Powered by BlockCypher & CoinGecko • Read-only, your keys are safe
```

---

## ✨ Commands

| Command | Description |
|---|---|
| `/wallet add <address>` | Link a Litecoin wallet address (read-only) |
| `/wallet remove <address>` | Unlink a wallet |
| `/wallet list` | Show all your linked wallets |
| `/portfolio` | Live balances + USD value for all wallets |

- Max **5 wallets** per user
- Supports `L...`, `M...`, and `ltc1...` address formats (including Exodus)
- All responses are **ephemeral** (only visible to you)

---

## 🚀 Setup

### 1. Clone the repo

```bash
git clone https://github.com/yourusername/ltc-portfolio-bot
cd ltc-portfolio-bot
```

### 2. Install dependencies

```bash
go mod tidy
```

### 3. Create your `.env` file

```bash
cp .env.example .env
```

Fill it in:

```env
DISCORD_TOKEN=your_bot_token_here
GUILD_ID=your_server_id_here
```

> Remove `GUILD_ID` (or leave it blank) to register commands globally — takes up to 1 hour to propagate. Keep it set during development for instant updates.

### 4. Run

```bash
go run main.go
```

---

## 🤖 Creating a Discord Bot

1. Go to [discord.com/developers/applications](https://discord.com/developers/applications)
2. Click **New Application** → give it a name
3. Go to **Bot** → click **Reset Token** → copy the token → paste into `.env`
4. Scroll down, enable **Server Members Intent**
5. Go to **OAuth2 → URL Generator**:
   - Scopes: `bot` + `applications.commands`
   - Bot permissions: `Send Messages`
6. Open the generated URL and invite the bot to your server

**Getting your Guild ID (Server ID):**
- Discord Settings → Advanced → enable **Developer Mode**
- Right-click your server icon → **Copy Server ID**

---

## 🔗 APIs Used

| Data | API | Key Required |
|---|---|---|
| Wallet balance & tx count | [BlockCypher](https://www.blockcypher.com/dev/litecoin/) | ❌ No |
| LTC/USD live price | [CoinGecko](https://www.coingecko.com/en/api) | ❌ No |

---

## 🗺️ Roadmap

- [ ] Persist wallets to SQLite (survive restarts)
- [ ] `/alert <price>` — DM when LTC hits a target
- [ ] Daily portfolio digest (scheduled cron)
- [ ] Multi-coin support (BTC, ETH...)
- [ ] `/history` — 7-day balance chart

---

## 📄 License

MIT © [bax](https://github.com/baxqc)

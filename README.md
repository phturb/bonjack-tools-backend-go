# bonjack-tools-backend-go

Backend for [bonjack-tools-website-v2](https://github.com/phturb/bonjack-tools-website-v2). It runs the Discord bot, the REST API and the websocket that drives the **Lois des Norms** lobby.

```sh
cp .env.example .env     # fill DISCORD_TOKEN, RIOT_API_KEY, database and JWT values
docker compose up -d     # postgres + adminer
go run .                 # listens on PORT (default 3001)
go test ./...
```

## Websocket contract

Endpoint: `ws://<host>/ws` (production `wss://tools.bonjack.club/ws/`). No authentication. The server holds a single global game state; every connected browser sees the same lobby.

### Envelope

Every frame in both directions is a JSON object:

```json
{ "action": "<string>", "content": "<string, optional>" }
```

`content` is itself a **JSON-encoded string**, not an object. Clients must `JSON.stringify` the payload before sending and `JSON.parse` it after receiving.

### Client → server actions

| `action` | `content` | Effect |
|---|---|---|
| `updatePlayers` | `[{ "id": "<discord id or empty>", "name"?: "<string>" }]`, at most 5 entries, index = seat | Re-seats the lobby. Ignored while a game is in progress (the server answers the sender with the current state). Unknown ids and duplicates become empty seats. `name` is ignored; the server uses the name it knows. |
| `roll` | none | Starts the game on the first call (creates the `games` row) and assigns a random role and champion to every seated player. Refused while `canRoll` is false. |
| `cancel` | none | Deletes the current game and its rolls from the database, then resets the lobby. |
| `reset` | none | Marks the current game finished (`finished_at`) and resets the lobby. The Discord button for this is labelled **Finish**. |
| `refreshDiscord` | none | Accepted, currently a no-op. |

Unknown actions are logged and dropped. The server never sends an error frame.

### Server → client action

| `action` | `content` |
|---|---|
| `updateState` | The full `GameState` below, JSON-encoded. |

`updateState` is sent to the new connection immediately after the upgrade, and broadcast to every connection whenever the state changes (seat change, roll, cancel, reset, player joining or leaving the voice channel, roll cooldown expiring).

### `GameState`

```jsonc
{
  "players": [                       // always 5 seats
    {
      "player":   { "id": "123", "name": "Alice" },   // id "" and name null for an empty seat
      "role":     "ADC" | "MID" | "JUNGLE" | "SUPPORT" | "TOP" | null,
      "champion": { "id": "266", "name": "Aatrox", "img": "Aatrox.png" } | null
    }
  ],
  "rollCount": 2,
  "gameInProgress": true,
  "availablePlayers": {              // everyone currently in the voice channel, keyed by discord id
    "123": {
      "id": "123",
      "name": "Alice",
      "stats": { "maxNumberOfRoll": 4, "numberOfGame": 12, "totalRoll": 31 }
    }
  },
  "gameId": 57,                      // 0 when no game is open
  "nextRollTimer": 287000,           // ms remaining before the next roll is allowed
  "canRoll": false,
  "discordGuild": "2123...",         // guild id
  "discordGuildName": "Bonjack",
  "discordGuildChannelId": "2123...",
  "discordGuildChannel": "General",  // channel name
  "leagueVersion": "14.21.1"
}
```

Notes for clients:

- `champion.img` is a file name. Build the icon URL as `https://ddragon.leagueoflegends.com/cdn/<leagueVersion>/img/champion/<img>`.
- While a game is in progress, seated players are injected into `availablePlayers` even if they left the voice channel, so the seat select always has a matching option.
- `nextRollTimer` is relative to the moment the frame was sent. Convert it to an absolute deadline on receipt; the server does not broadcast every tick, only when `canRoll` flips back to true.
- The server does not send a frame when it refuses an action (`roll` during cooldown, `updatePlayers` during a game). The client keeps the last `updateState`.

### Sequence

```mermaid
sequenceDiagram
    autonumber
    participant B as Browser (website-v2)
    participant S as Backend /ws
    participant GM as GameManager
    participant DB as Postgres
    participant D as Discord

    B->>S: WebSocket upgrade GET /ws
    S->>GM: HandleWebsocketConnection(conn)
    GM-->>B: {action:"updateState", content:GameState}

    Note over D,GM: Voice channel events update the lobby independently of the browser
    D->>GM: VoiceStateUpdate (join)
    GM->>DB: upsert player, read stats
    GM-->>B: updateState (availablePlayers + seat filled)

    B->>S: {action:"updatePlayers", content:"[{id,name}...]"}
    S->>GM: handleUpdatePlayers
    alt game not in progress
        GM-->>B: updateState (broadcast to all)
    else game in progress
        GM-->>B: updateState (sender only, unchanged)
    end

    B->>S: {action:"roll"}
    S->>GM: handleRoll
    GM->>DB: read champion pools
    GM->>DB: tx: insert games/game_players (first roll), insert game_player_rolls
    DB-->>GM: commit
    GM->>GM: rollCount++, canRoll=false, nextRollTimer=TIMER_TIME
    GM-->>B: updateState (broadcast)
    GM->>D: edit tracked /ldn message

    loop every second
        GM->>GM: nextRollTimer -= 1000
    end
    GM-->>B: updateState (canRoll=true, broadcast once)

    B->>S: {action:"reset"}
    S->>GM: handleReset
    GM->>DB: update games set finished_at
    GM-->>B: updateState (lobby cleared, gameInProgress=false)

    B->>S: {action:"cancel"}
    S->>GM: handleCancel
    GM->>DB: tx: delete game_player_rolls, game_players, games
    GM-->>B: updateState (lobby cleared)

    B-xS: connection closed
    S->>GM: HandleWebsocketConnection(conn, nil) → removed from broadcast list
```

Restart behaviour: on boot `bootstrap()` reloads the last game with `finished_at IS NULL` from the database and restores seats, roles and champions from its latest roll, so a deploy during a game does not lose it. The roll cooldown and the tracked Discord message are not persisted.

## REST API

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/healthz` | – | `{"ok":true}` |
| GET | `/api/players` | – | all known players |
| GET | `/api/games` | – | all games |
| GET | `/api/players/{id}/stats` | – | `404` for unknown player |
| GET | `/api/players/{id}/champions` | – | pool with `owned` flag; no rows stored means every champion is owned |
| POST | `/api/players/{id}/champions` | Bearer JWT (Auth0) | body `[{"id":"266","owned":true}]`; subject must be `oauth2|discord|{id}` |

JWT is enforced only when `JWT_SECRET` is set. CORS allows any origin with `Content-Type` and `Authorization` headers.

## Swiss tournament API

Swiss-system tournaments between individual players, spec in `bonjack-tools-website-v2/docs/specs/swiss-tournament.md`. Reads are public. Every mutation requires a Bearer JWT (when `JWT_SECRET` is set) and is restricted to the creator: the token `sub` is stored as `ownerSub` at creation and compared on each call (`403` otherwise). Without JWT configured (local development, `go run ./cmd/swissdev`) the caller identity is the raw `Authorization: Bearer <subject>` value, or `dev` when no token is sent; this lets a local frontend act as the owner or as a stranger without Auth0. Errors are `{ "error": "message" }`.

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/api/swiss` | – | `[TournamentSummary]`: public tournaments, newest first |
| POST | `/api/swiss` | `TournamentConfig` | `Tournament`, `201` |
| GET | `/api/swiss/{id}` | – | `Tournament` with participants, rounds, matches, standings |
| PATCH | `/api/swiss/{id}` | partial `TournamentConfig` | `Tournament`; after start only `name` and `description` may change (`409`) |
| DELETE | `/api/swiss/{id}` | – | `204` |
| PUT | `/api/swiss/{id}/participants` | `[{ "id"?, "name", "seed" }]` | `Tournament`; full replace, draft only, seeds renumbered 1..n |
| POST | `/api/swiss/{id}/participants/{pid}/drop` | – | `Tournament`; in progress only, forfeits the unplayed current match |
| POST | `/api/swiss/{id}/start` | – | `Tournament`; needs 2+ participants, applies seeding, generates round 1 |
| PUT | `/api/swiss/{id}/matches/{mid}/result` | `{ "p1Score", "p2Score" }` | `Tournament`; current round only, scores must form a valid best-of winner (`422`) |
| POST | `/api/swiss/{id}/rounds/close` | – | `Tournament`; every current match resolved, then next round; the Swiss stage ends as `completed`, the playoffs as `finished` |
| POST | `/api/swiss/{id}/playoffs/start` | – | `Tournament`; `completed` only, needs 2+ advanced participants; single-elimination round 1 seeded by the standings (`playoffsBestOf`, byes for top seeds), status `playoffs` |
| POST | `/api/swiss/{id}/rounds/undo` | – | `Tournament`; in progress only, removes the latest round when it has no result (`409` otherwise) |

`TournamentConfig` is flat: `{ "name", "description", "winsToAdvance", "lossesToEliminate", "bestOf", "decidersBestOf", "seeding", "allowRematches", "visibility" }`. Defaults: 3 / 3 / 1, `decidersBestOf` = `bestOf`, `manual`, `false`, `public`.

`TournamentSummary` is `{ "id", "name", "status", "participants", "createdAt" }` where `participants` is the participant count.

Wins, losses, Buchholz, participant status and standings are derived from the match rows on every read. Pairing is deterministic: buckets by record, Buchholz then seed inside a bucket, top half vs bottom half with rematch avoidance, floaters for odd buckets, one bye per participant per tournament, `decidersBestOf` for matches where a participant is one result from advancing or elimination. Random seeding uses the `rng_seed` stored at creation.

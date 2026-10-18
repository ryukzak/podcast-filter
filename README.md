# Podcast Filter

[![CI](https://github.com/ryukzak/podcast-filter/actions/workflows/build.yaml/badge.svg)](https://github.com/ryukzak/podcast-filter/actions/workflows/build.yaml)
[![Go version](https://img.shields.io/github/go-mod/go-version/ryukzak/podcast-filter)](go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/ryukzak/podcast-filter)](https://goreportcard.com/report/github.com/ryukzak/podcast-filter)
[![Docker Image](https://img.shields.io/docker/image-size/ryukzak/podcast-filter/latest?label=docker)](https://hub.docker.com/r/ryukzak/podcast-filter)
[![License](https://img.shields.io/github/license/ryukzak/podcast-filter)](LICENSE)

This microservice filters podcast feeds based on provided regular expressions.

Open the service in a browser and you get a builder: paste a feed URL, write the
patterns, and watch — live — which episodes pass and which get dropped. When you
are happy with the result, copy the URL and subscribe to it in your podcast
client.

You can try the service here: <http://podcast-filter.swampbuds.me>

## Usage

### Running the Docker Image

The easiest way to use the Podcast Filter is by pulling the Docker image and running it in a container:

``` shell
docker run --name podcast-filter -p8080:8080 -eBASE_URL='localhost:8080' \
    -v podcast-filter-data:/data ryukzak/podcast-filter
```

The server will start running inside the Docker container on port 8080. The
volume keeps the short links (see below) across restarts.

### The Builder

<http://localhost:8080/> explains how filtering works and lets you assemble a
feed interactively:

- the preview list shows *every* episode of the origin feed, one per line, with
  its date and length, marked green when it passes and red when it is dropped;
- clicking a line unrolls the details: the full title, the description, and what
  each pattern made of this episode — including the one that dropped it;
- an all / only passing / only removed switch narrows the list, though showing
  all of them is the default, since that is what lets you watch one episode
  react to a pattern;
- rows are compact one-liners and the list is resizable by dragging its bottom
  edge, so a long feed can be scanned in big chunks;
- it re-evaluates on every keystroke using the same Go engine the real feed uses,
  so `(?i)` and other RE2 features behave exactly as they will in production;
- the list scrolls independently of the page and holds its position while you
  edit, so you can keep one specific episode in view and watch it react;
- pasting an existing filtered URL, a link id, or just a plain feed URL into
  "Start from an existing URL" loads it for editing and debugging;
- the links you create, copy, update or open are listed there under "Recently
  used", so you can reopen one in a click instead of digging it out of your
  podcast client. The list is kept in the browser's local storage, never on the
  server.

### Filtering a Podcast Feed

To filter a podcast feed, you need to make an HTTP GET request to the /filter endpoint of the server with the following query parameters:

- `feed`: The URL of the podcast feed.
- `title`: (Optional) The title to be displayed for the filtered feed. If not provided, a default title will be used.
- `re`: (Multiple parameters) Regular expression ([go regexp](https://pkg.go.dev/regexp)) to filter the podcast episodes. See [how the patterns combine](#how-the-patterns-combine).
- `neg`: (Multiple parameters) Boolean value(s) indicating whether the corresponding regular expression pattern should be negated (i.e., exclude episodes matching the pattern). Default: false (if neg is used once, it should be used for all RE in the query).
- `min`, `max`: (Optional) Episode length bounds in minutes, read from `itunes:duration`. Episodes outside them are dropped whatever the patterns say; episodes without a duration always pass.

Example Request:

``` http
GET /filter?feed=https://feedmaster.umputun.com/rss/echo-msk&title=Эхо (часть)&re=(?i)(Шульман)
```

The filtered feed is the origin feed with the dropped episodes cut out: the
artwork, the author, the categories and every episode's details come through as
they are. Only a few things change:

- the title gets " (filtered)" appended, unless `title` is set;
- the description starts with a link that opens the filter in the builder,
  followed by the original description;
- `atom:link` and `itunes:new-feed-url` are removed, since they point podcast
  clients back to the unfiltered origin feed.

It is possible to have several regular expressions. For example, you can remove
something from the feed and filter what is left:

``` http
GET /filter?feed=https://feedmaster.umputun.com/rss/echo-msk&title=Эхо (часть)&re=(?i)(LIVE)&neg=true&re=(?i)(Шульман)&neg=false
```

### How the patterns combine

Every pattern is matched against the episode title. Patterns are **not** AND'd:

- **keep patterns (`neg=false`) are alternatives.** An episode is in as soon as
  it matches any one of them, so `re=(?i)(Шульман)&re=(?i)(Наки)` serves episodes
  about either person. With no keep pattern at all, every episode is in.
- **drop patterns (`neg=true`) are vetoes.** An episode matching any drop pattern
  is out, even when a keep pattern wanted it.

So an episode is served when it matches at least one keep pattern (or there are
none) *and* matches no drop pattern. The order of the patterns never matters.

To require two things at once, put them in a single pattern rather than two:
`(?i)(?:шульман).*(?:итоги)` matches both, while two separate keep patterns match
either.

### Short URLs

Some podcast clients silently truncate long URLs, and a filter with a few Unicode
patterns gets long quickly — a single Cyrillic letter costs six characters once
percent-encoded. When the URL grows past 512 characters, the builder suggests
storing the filter on the server instead and gives you a short URL:

``` http
GET /filter?id=bc1970b7-895b-47ac-9d2e-ae018c890a0c
```

The filter behind such a URL is kept in a [bbolt](https://github.com/etcd-io/bbolt)
file. Pasting the short URL into the builder loads its filter back, so a stored
filter stays debuggable.

If you set a password when creating a short URL, you can later change the filter
it serves without changing the URL itself — handy once the URL is already in your
podcast client. Without a password the link is immutable. The password is only
ever used to authorize an update: it cannot be changed or recovered, so a
forgotten password means creating a new short URL.

### HTTP API

The builder is a plain client of these endpoints:

| Endpoint | Description |
| --- | --- |
| `POST /api/preview` | `{"filter": {...}}` → every episode with its length, per-pattern match results, and the pattern that dropped it. |
| `POST /api/links` | `{"filter": {...}, "password": "…"}` → a new short link. An empty password makes it immutable. |
| `GET /api/links/{id}` | The stored filter, and whether it can be updated. |
| `PUT /api/links/{id}` | `{"filter": {...}, "password": "…"}` → replaces the stored filter. |

A filter is `{"feed": "…", "title": "…", "rules": [{"re": "…", "neg": false}], "minMinutes": 0, "maxMinutes": 0}`.

## Problems

- First of all, test your URL using [HTTPie](https://httpie.io) or in a web browser.
- Some podcast clients may truncate long feed URLs without displaying any useful alert or error messages. Unfortunately, manually requesting the feed can still work fine. In such cases, use a short URL.

## Customization

### Environment Variables

The behavior of the Podcast Filter application can be customized using the following environment variables:

- `BASE_URL`: The public URL of the service, used for the links inside the filtered feed. By default, they are built from the host the request was sent to.
- `DB_PATH`: Where to keep the short-link database. Defaults to `podcast-filter.db` in the working directory, and to `/data/podcast-filter.db` in the Docker image.
- `PORT`: Port to listen on. Defaults to `8080`.

## Development

``` shell
go test ./...
go run .
```

The builder's assets live in `web/` and are embedded into the binary, so the
build has no separate frontend step.

CI also runs the linters; run them locally with:

``` shell
golangci-lint run ./...          # Go, configured in .golangci.yml
npx @biomejs/biome ci .          # web/, configured in biome.json
```

## Contributing

Contributions to the Podcast Filter project are welcome! If you find a bug, have suggestions for improvements, or would like to add new features, please open an issue or submit a pull request.

## License

The Podcast Filter application is open-source and available under the BSD License.

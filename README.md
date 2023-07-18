# Podcast Filter

This microservice filters podcast feeds based on a provided regular expression.

## Usage

### Running the Docker Image

The easiest way to use the Podcast Filter is by pulling the Docker image and running it in a container:

``` shell
docker run -p 8080:8080 -eBASE_URL=localhost:8080 ryukzak/podcast-filter
```

The server will start running inside the Docker container on port 8080.

### Filtering a Podcast Feed

To filter a podcast feed, you need to make an HTTP GET request to the /filter endpoint of the server with the following query parameters:

- `feed`: The URL of the podcast feed.
- `title`: (Optional) The title to be displayed for the filtered feed. If not provided, a default title will be used.
- `re`: (Multiple parameters) Regular expression ([go regexp](https://pkg.go.dev/regexp)) to filter the podcast episodes.
- `neg`: (Multiple parameters) Boolean value(s) indicating whether the corresponding regular expression pattern should be negated (i.e., exclude episodes matching the pattern). Default: false (if neg is used once, it should be used for all RE in the query).

Example Request:

``` http
GET /filter?feed=https://feedmaster.umputun.com/rss/echo-msk&title=Эхо (часть)&re=(?i)(Шульман)
```

It is possible to have a sequence of regular expressions. For example, you can specify something to remove from the feed and only then apply the filtering:

``` http
GET /filter?feed=https://feedmaster.umputun.com/rss/echo-msk&title=Эхо (часть)&re=(?i)(LIVE)&neg=true&re=(?i)(Шульман)&neg=false
```

## Customization

### Environment Variables

The behavior of the Podcast Filter application can be customized using the following environment variables:

- `BASE_URL`: The base URL to be used for constructing the filtered feed's link. By default, it is not set.

## Contributing

Contributions to the Podcast Filter project are welcome! If you find a bug, have suggestions for improvements, or would like to add new features, please open an issue or submit a pull request.

## License

The Podcast Filter application is open-source and available under the BSD License.

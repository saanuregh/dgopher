# Redis

Open the key browser from a Redis connection's menu.

![A Redis database: the key tree with its folders, a hash opened beside it,
and the console, monitor and other panels below](../images/redis.png)

## Keys

- A key tree read with `SCAN`, grouped into folders by `:` (or the
  separator the connection sets), with pattern and type filters. F5 scans
  again.
- Set a TTL, rename, delete and create keys. On Redis 7.4 and later, each
  hash field's TTL can be set or removed.
- **Delete by pattern:** counts and names the keys first, always asks (with
  the connection's name typed on production), unlinks them in batches as a
  scan finds them, and is audited.
- **Export** the keys matching a pattern to a JSON Lines file, each with its
  type, TTL and value (base64 where it is not text). **Import** such a file:
  each key is written in one transaction, keys already there are kept or
  replaced as you choose, through the safety policy, and audited. A hash
  field's own TTL and a stream's consumer groups are not exported.

## Values

- Viewers and editors for strings (with JSON formatting), hashes, lists,
  sets, sorted sets and streams.
- **Streams:** add and delete entries, create and destroy consumer groups,
  and see each group's consumers and pending entries, which you can
  acknowledge or claim for another consumer.
- **Lists:** an item is removed at its own index, never another item of the
  same value.
- **Large values:** a key's items are read 500 at a time, with more on
  request. A string's first 64 KB is read, and the rest on request. A string
  shown in part cannot be edited, because saving would cut it.
- **Redis 8 types:**
  - JSON documents, edited whole, formatted and checked as JSON before
    `JSON.SET` saves them.
  - Arrays: values read in index order, set at an index, appended, and
    removed.
  - Vector sets: elements with their attributes; add elements with their
    vectors and remove them; the chosen element shows its vector, its
    attributes to edit, and the elements most like it.
  - Time series: charted over the last hour, day, week or month, or all of
    it, as the averages of about 500 buckets, with labels, rules and sizes;
    add samples.
  - Other types, such as Bloom filters, point you to the console.
- **Decoded values:** values stored compressed or serialized show decoded,
  read-only: gzip, zlib, zstd, LZ4, Snappy and Brotli (found by their first
  bytes, or chosen); MessagePack, Python's pickle (never running the code it
  names), PHP's serialize, Java serialization, and Protocol Buffers without
  a schema (as JSON or as `protoc --decode_raw` shows them), inside any of
  the compressions. A string decodes whole; a hash's, list's or set's chosen
  item decodes below the items.

## Search

The Search panel lists the server's search indexes, each with what it
indexes, its fields and its documents. Run queries on an index; the
documents found open as keys. **New Index** writes `FT.CREATE` in the
console for you to complete, and **Drop Index** drops one, keeping its
keys.

## Console

- Replies look the way `redis-cli` shows them. ↑ and ↓ walk the history.
- As you type, the commands it may be show, and Tab completes the name.
  Once a command is named, its syntax and summary show, from the server's
  `COMMAND DOCS` (Redis 7 and later).
- `KEYS` asks first, because it stalls a large server; the tree uses
  `SCAN`.
- **Run a command file:** one command a line, `#` starting a comment. Each
  command goes through the safety policy first; if any writes, the file
  asks once. They run in the console one after another, audited, stopping
  at the first failure.

## Panels

Below the keys, beside the console:

- **Monitor:** a live feed of the commands the server runs (`MONITOR`, on a
  connection of its own), for the connection's database or all of them,
  filtered as you type, with secrets hidden. It asks first on production,
  because it slows a busy server.
- **Slow log:** slowest first, with a reset of every master's log.
- **Memory:** an analysis of up to 10,000, 100,000 or 1,000,000 keys
  (asking first on production). It reads each key's type, size and TTL as
  `SCAN` finds them, and sums them into the largest keys, the namespaces
  before the key separator, the types, and when keys expire.
- **Pub/Sub:** listen to channels and patterns, with messages showing as
  they come; publish to a channel, through the safety policy; and see the
  channels clients listen to, with how many listen, on every node of a
  cluster. Sharded channels (`SSUBSCRIBE`) are not supported.

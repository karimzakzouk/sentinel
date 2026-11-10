package logs

// Stack traces as each runtime prints them, used across this package's tests.
const (
	goPanicTrace = `panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x4a3b2c]

goroutine 1 [running]:
main.(*server).handle(0x0, {0x7f8a12, 0x3})
	/app/server.go:42 +0x1d
net/http.HandlerFunc.ServeHTTP(0xc000123456, {0x8d1f20, 0xc0001a2b3c}, 0xc0001b4000)
	/usr/local/go/src/net/http/server.go:2136 +0x29
main.main()
	/app/main.go:17 +0x5b
exit status 2`

	javaTrace = `Exception in thread "main" java.lang.IllegalStateException: Failed to load ApplicationContext for [WebMergedContextConfiguration@4c2bb6e0]
	at org.springframework.test.context.cache.DefaultCacheAwareContextLoaderDelegate.loadContext(DefaultCacheAwareContextLoaderDelegate.java:180)
	at com.example.shop.App.main(App.java:10)
Caused by: java.lang.NullPointerException: Cannot invoke "String.length()" because "name" is null
	at com.example.shop.Catalog.load(Catalog.java:42)
	at com.example.shop.Catalog.<init>(Catalog.java:20)
	at java.base/java.lang.reflect.Constructor.newInstance(Constructor.java:480)
	... 12 more`

	pythonTrace = `Traceback (most recent call last):
  File "/app/main.py", line 10, in <module>
    main()
  File "/app/main.py", line 6, in main
    cart.connect()
  File "/usr/local/lib/python3.11/site-packages/redis/connection.py", line 707, in connect
    raise ConnectionError(self._error_message(e))
redis.exceptions.ConnectionError: Error 111 connecting to redis-cart:6379. Connection refused.`

	pythonChainedTrace = `Traceback (most recent call last):
  File "/app/config.py", line 4, in load
    return int(os.environ["PORT"])
KeyError: 'PORT'

During handling of the above exception, another exception occurred:

Traceback (most recent call last):
  File "/app/main.py", line 3, in <module>
    config.load()
  File "/app/config.py", line 6, in load
    raise RuntimeError("PORT is not set")
RuntimeError: PORT is not set`

	nodeTrace = `TypeError: Cannot read properties of undefined (reading 'code')
    at convert (/usr/src/app/server.js:162:31)
    at /usr/src/app/server.js:110:7
    at Layer.handle [as handle_request] (/usr/src/app/node_modules/express/lib/router/layer.js:95:5)
    at process.processTicksAndRejections (node:internal/process/task_queues:95:5)

Node.js v20.11.1`

	dotnetTrace = `Unhandled exception. System.InvalidOperationException: Sequence contains no elements
   at System.Linq.ThrowHelper.ThrowNoElementsException()
   at System.Linq.Enumerable.First[TSource](IEnumerable` + "`" + `1 source)
   at cartservice.cartstore.RedisCartStore.GetCartAsync(String userId) in /app/cartstore/RedisCartStore.cs:line 87
   at cartservice.services.CartService.GetCart(GetCartRequest request, ServerCallContext context) in /app/services/CartService.cs:line 43`

	loggedJavaTrace = `2026-11-08 21:14:11.123 ERROR 1 --- [main] o.s.boot.SpringApplication : Application run failed
	at org.springframework.boot.SpringApplication.run(SpringApplication.java:320)
	at com.example.shop.App.main(App.java:10)`

	// Online Boutique's cart service with Redis gone, as .NET's console
	// logger writes it: the level and category, then the message indented.
	dotnetConsoleTrace = `info: Grpc.AspNetCore.Server.ServerCallHandler[7]
      Error status code 'FailedPrecondition' with detail 'Can't access cart storage. StackExchange.Redis.RedisConnectionException: The message timed out in the backlog attempting to send because no connection became available, command=HMGET, timeout: 5000'
       ---> StackExchange.Redis.RedisConnectionException: It was not possible to connect to the redis server(s). ConnectTimeout
         --- End of inner exception stack trace ---
         at Microsoft.Extensions.Caching.StackExchangeRedis.RedisCache.GetAndRefreshAsync(String key, Boolean getData, CancellationToken token)
         at Microsoft.Extensions.Caching.StackExchangeRedis.RedisCache.GetAsync(String key, CancellationToken token)
         at cartservice.cartstore.RedisCartStore.GetCartAsync(String userId) in /app/cartstore/RedisCartStore.cs:line 90' raised.`

	rubyTrace = "/app/lib/shop.rb:4:in 'Shop#load': undefined method '[]' for nil (NoMethodError)\n" +
		"\tfrom /usr/local/bundle/gems/rack-3.1.0/lib/rack/builder.rb:12:in 'Rack::Builder#call'\n" +
		"\tfrom /app/lib/shop.rb:10:in 'block in Shop.run'\n" +
		"\tfrom /app/app.rb:3:in '<main>'"

	rubyOldTrace = "/app/lib/shop.rb:4:in `load': undefined method `[]' for nil:NilClass (NoMethodError)\n" +
		"\tfrom /app/app.rb:3:in `<main>'"

	phpTrace = `PHP Fatal error:  Uncaught Error: Call to a member function load() on null in /var/www/src/Catalog.php:12
Stack trace:
#0 /var/www/src/Shop.php(30): App\Catalog->items()
#1 /var/www/vendor/slim/slim/Slim/App.php(205): App\Shop->run()
#2 /var/www/public/index.php(8): Slim\App->handle()
#3 {main}
  thrown in /var/www/src/Catalog.php on line 12`

	rustTrace = `thread 'main' panicked at src/catalog.rs:17:9:
index out of bounds: the len is 0 but the index is 0
stack backtrace:
   0: rust_begin_unwind
             at /rustc/90b35a6239c3d8bdabc530a6a0816f7ff89a0aaf/library/std/src/panicking.rs:665:5
   1: core::panicking::panic_bounds_check
             at /rustc/90b35a6239c3d8bdabc530a6a0816f7ff89a0aaf/library/core/src/panicking.rs:266:5
   2: shop::catalog::Catalog::first
             at ./src/catalog.rs:17:9
   3: shop::main
             at ./src/main.rs:6:5
note: Some details are omitted, run with ` + "`RUST_BACKTRACE=full`" + ` for a verbose backtrace.`

	rustShortTrace = `thread 'main' panicked at 'called ` + "`Option::unwrap()`" + ` on a ` + "`None`" + ` value', src/main.rs:6:31
note: run with ` + "`RUST_BACKTRACE=1`" + ` environment variable to display a backtrace`

	glibcAssertTrace = "shop: src/catalog.c:12: load: Assertion `items != NULL' failed."

	muslAssertTrace = "Assertion failed: items != NULL (src/catalog.cpp: int shop::Catalog::load(int): 12)"

	abseilTrace = `*** SIGSEGV received at time=1762736400 on cpu 2 ***
PC: @     0x55d1c2a3b1c4  (unknown)  shop::Catalog::Load()
    @     0x7f3a2b1c4d5e       64  __libc_start_main
    @     0x55d1c2a3b2d0       32  main`

	backwardTrace = `Stack trace (most recent call last):
#2    Object "/app/shop", at 0x55d1c2a3b2d0, in main
      Source "/src/main.cpp", line 20, in main [0x55d1c2a3b2d0]
#1    Object "/app/shop", at 0x55d1c2a3b250, in shop::Catalog::run()
      Source "/src/catalog.cpp", line 31, in shop::Catalog::run() [0x55d1c2a3b250]
#0    Object "/app/shop", at 0x55d1c2a3b1c4, in shop::Catalog::load(int)
      Source "/src/catalog.cpp", line 12, in shop::Catalog::load(int) [0x55d1c2a3b1c4]`
)

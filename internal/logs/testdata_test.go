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
)

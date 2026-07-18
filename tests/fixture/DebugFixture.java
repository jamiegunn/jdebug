// DebugFixture — a pure-JDK stand-in for the Spring Boot app jdebug targets.
// No dependencies, no build: `java DebugFixture.java [port]` (JDK 11+).
//
// It serves the actuator endpoints jdebug's tier 1 uses, with AUTHENTIC
// payloads: /actuator/threaddump returns the real HotSpot jcmd Thread.print
// output (via the DiagnosticCommand MBean) and /actuator/heapdump streams a
// real hprof (via HotSpotDiagnosticMXBean) — so a capture exercised against
// this fixture validates jdebug against genuine JVM formats, not mocks.
// It can also injure itself on demand: /deadlock parks two threads in a
// classic lock cycle, /hot spins CPU burners — the analyzer's targets.
//
// Used by tests/live/run-live-tests.sh (same-host, kubectl shimmed) and by
// the kind integration job (mounted via ConfigMap into a stock temurin pod).

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import javax.management.ObjectName;
import java.io.File;
import java.io.IOException;
import java.io.OutputStream;
import java.lang.management.ManagementFactory;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

public class DebugFixture {
    static final Object LOCK_A = new Object();
    static final Object LOCK_B = new Object();
    static volatile boolean deadlocked = false;

    public static void main(String[] args) throws Exception {
        int port = args.length > 0 ? Integer.parseInt(args[0]) : 8080;
        HttpServer s = HttpServer.create(new InetSocketAddress(port), 0);

        s.createContext("/actuator/health", ex ->
            respond(ex, 200, "{\"status\":\"UP\",\"components\":{\"fixture\":{\"status\":\"UP\"}}}"));

        s.createContext("/actuator/metrics", ex ->
            respond(ex, 200, "{\"names\":[\"jvm.memory.used\",\"jvm.gc.pause\",\"process.cpu.usage\"]}"));

        // the REAL jcmd Thread.print output — exactly what tier 2 would get
        s.createContext("/actuator/threaddump", ex -> {
            try {
                String dump = (String) ManagementFactory.getPlatformMBeanServer().invoke(
                    new ObjectName("com.sun.management:type=DiagnosticCommand"),
                    "threadPrint",
                    new Object[]{new String[]{"-l"}},
                    new String[]{String[].class.getName()});
                respond(ex, 200, dump);
            } catch (Exception e) {
                respond(ex, 500, "threadPrint failed: " + e);
            }
        });

        // a REAL hprof, written by the JVM itself
        s.createContext("/actuator/heapdump", ex -> {
            try {
                Path tmp = Files.createTempFile("fixture-heap", ".hprof");
                Files.delete(tmp); // dumpHeap requires the file NOT to exist
                var mx = ManagementFactory.getPlatformMXBean(
                    com.sun.management.HotSpotDiagnosticMXBean.class);
                mx.dumpHeap(tmp.toString(), true);
                byte[] heap = Files.readAllBytes(tmp);
                Files.delete(tmp);
                ex.sendResponseHeaders(200, heap.length);
                try (OutputStream os = ex.getResponseBody()) { os.write(heap); }
            } catch (Exception e) {
                respond(ex, 500, "dumpHeap failed: " + e);
            }
        });

        // self-injury: a genuine two-lock deadlock (idempotent)
        s.createContext("/deadlock", ex -> {
            if (!deadlocked) {
                deadlocked = true;
                new Thread(() -> grab(LOCK_A, LOCK_B), "fixture-deadlock-1").start();
                new Thread(() -> grab(LOCK_B, LOCK_A), "fixture-deadlock-2").start();
                sleep(500); // let them interlock before we answer
            }
            respond(ex, 200, "deadlocked\n");
        });

        // self-injury: N spinning threads (default 4)
        s.createContext("/hot", ex -> {
            for (int i = 0; i < 4; i++) {
                Thread t = new Thread(DebugFixture::spin, "fixture-hot-" + i);
                t.setDaemon(true);
                t.start();
            }
            respond(ex, 200, "spinning\n");
        });

        s.start();
        System.out.println("DebugFixture listening on :" + port + " (pid " + ProcessHandle.current().pid() + ")");
    }

    static void grab(Object first, Object second) {
        synchronized (first) {
            sleep(200);
            synchronized (second) { sleep(1); }
        }
    }

    static volatile long sink;
    static void spin() { long x = 0; while (true) { x += System.nanoTime() % 7; sink = x; } }

    static void sleep(long ms) { try { Thread.sleep(ms); } catch (InterruptedException ignored) {} }

    static void respond(HttpExchange ex, int code, String body) throws IOException {
        byte[] b = body.getBytes(StandardCharsets.UTF_8);
        ex.sendResponseHeaders(code, b.length);
        try (OutputStream os = ex.getResponseBody()) { os.write(b); }
    }
}

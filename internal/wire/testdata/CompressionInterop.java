// Calls official Connector/J codecs directly; no database or copied codec implementation.
import com.oceanbase.jdbc.internal.io.input.DecompressPacketInputStream;
import com.oceanbase.jdbc.internal.io.output.CompressPacketOutputStream;
import com.oceanbase.jdbc.internal.protocol.TimeTrace;
import com.oceanbase.jdbc.util.Options;
import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Arrays;

class CompressionInterop {
    public static void main(String[] args) throws Exception {
        Path dir = Path.of(args[0]);
        Options options = new Options();
        int count = 0;
        try (var files = Files.newDirectoryStream(dir, "*.payload")) {
            for (Path file : files) {
                String name = file.getFileName().toString().replace(".payload", "");
                byte[] payload = Files.readAllBytes(file);
                byte[] goFrame = Files.readAllBytes(dir.resolve(name + ".go"));
                var input = new ByteArrayInputStream(goFrame);
                var reader = new DecompressPacketInputStream(input, 1, options, new TimeTrace(1, 0));
                if (!Arrays.equals(reader.getPacketArray(false), payload)) {
                    throw new AssertionError(name + ": Go encode -> Java decode differs");
                }
                if (!Arrays.equals(reader.getPacketArray(false), new byte[]{0x42}) || input.available() != 0) {
                    throw new AssertionError(name + ": following message differs");
                }
                var output = new ByteArrayOutputStream();
                var writer = new CompressPacketOutputStream(output, 1, options, new TimeTrace(1, 0));
                writer.startPacket(0);
                if (payload.length == 0) {
                    writer.writeEmptyPacket();
                } else {
                    writer.write(payload);
                    writer.flush();
                }
                byte[] javaFrame = output.toByteArray();
                if (payload.length < 0xFFFFFF && compressed(goFrame) != compressed(javaFrame)) {
                    throw new AssertionError(name + ": compression decision differs from official Java");
                }
                Files.write(dir.resolve(name + ".java"), javaFrame);
                count++;
            }
        }
        System.out.println("Official Connector/J: " + count + " Go messages decoded; Java frames generated");
    }

    private static boolean compressed(byte[] frame) {
        return frame[4] != 0 || frame[5] != 0 || frame[6] != 0;
    }
}

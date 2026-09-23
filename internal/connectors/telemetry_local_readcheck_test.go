package connectors
import("context";"encoding/json";"os";"testing";"time")
func TestLocalReadcheck(t *testing.T){b,_:=os.ReadFile("../../.local/live-integration/telemetry.json");var c TelemetryConfig;json.Unmarshal(b,&c);tm,e:=NewTelemetry(c);if e!=nil{t.Fatal(e)};end:=time.Now().UTC().Truncate(time.Millisecond);out,e:=tm.QueryLogsOptions(context.Background(),"lab-order-api","loki","","",false,TimeWindow{Start:end.Add(-30*time.Minute),End:end},1);if e!=nil{t.Fatal(e)};_,e=tm.lokiSummary(context.Background(),out.Source);if e!=nil{t.Fatal(e)}}

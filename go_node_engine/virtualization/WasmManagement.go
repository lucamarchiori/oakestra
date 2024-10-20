package virtualization

import (
	//"context"
	//"errors"
	//"fmt"

	"go_node_engine/logger"
	"go_node_engine/model"

	//"os"
	"reflect"
	//"strconv"
	//"strings"
	"sync"
	"time"

	wasmtime "github.com/bytecodealliance/wasmtime-go/v25"
	//"github.com/struCoder/pidusage"
)

type WasmRuntime struct {
	killQueue   map[string]*chan bool
	channelLock *sync.RWMutex
}

var wasmRuntime = WasmRuntime{
	channelLock: &sync.RWMutex{},
}

var wasmSingletonOnce sync.Once

func GetWasmRuntime() *WasmRuntime {
	logger.InfoLogger().Print("Getting WASM runtime")
	wasmSingletonOnce.Do(func() {
		wasmRuntime.killQueue = make(map[string]*chan bool)
		model.GetNodeInfo().AddSupportedTechnology(model.WASM_RUNTIME)
	})
	return &wasmRuntime
}

func (r *WasmRuntime) StopWasmRuntime() {
	logger.InfoLogger().Print("Stopping WASM runtime")
	r.channelLock.Lock()
	taskIDs := reflect.ValueOf(r.killQueue).MapKeys()
	r.channelLock.Unlock()

	for _, taskid := range taskIDs {
		logger.InfoLogger().Print(taskid)

		//err := r.Undeploy(extractWasmSnameFromTaskID(taskid.String()), extractWasmInstanceNumberFromTaskID(taskid.String()))
		//if err != nil {
		//	logger.ErrorLogger().Printf("Unable to undeploy %s, error: %v", taskid.String(), err)
		//}
	}
	logger.InfoLogger().Print("WASM runtime stopped")
}

func (r *WasmRuntime) Deploy(service model.Service, statusChangeNotificationHandler func(service model.Service)) error {
	// TODO

	logger.InfoLogger().Print("Deploying WASM service...")
	// To test the execution of the service, fetch the WASM module from local files

	engine := wasmtime.NewEngine()
	wasiConfig := wasmtime.NewWasiConfig()
	wasiConfig.InheritStdout() // To inherit stdout for printing
	store := wasmtime.NewStore(engine)
	store.SetWasi(wasiConfig)

	logger.InfoLogger().Print("Created Wasmtime engine and store with WASI support")

	return nil
}

func (r *WasmRuntime) Undeploy(service string, instance int) error {
	// TODO
	logger.InfoLogger().Print("Undeploying WASM service")
	logger.InfoLogger().Print(service)
	logger.InfoLogger().Print(instance)
	return nil
}

func (r *WasmRuntime) ResourceMonitoring(every time.Duration, notifyHandler func(res []model.Resources)) {
	for {
		select {
		case <-time.After(every):
			resourceList := make([]model.Resources, 0)
			r.channelLock.RLock()
			// for taskid, module := range r.modules {
			// 	// Get CPU and memory stats based on pid
			// 	sysInfo, err := pidusage.GetStat(module.Pid)
			// 	if err != nil {
			// 		logger.ErrorLogger().Printf("Unable to fetch task info: %v", err)
			// 		continue
			// 	}
			// 	resourceList = append(resourceList, model.Resources{
			// 		Cpu:      fmt.Sprintf("%f", sysInfo.CPU),
			// 		Memory:   fmt.Sprintf("%f", sysInfo.Memory),
			// 		Disk:     fmt.Sprintf("%d", 0),
			// 		Sname:    module.Sname,
			// 		Logs:     getWasmLogs(taskid),
			// 		Runtime:  string(model.WASM_RUNTIME),
			// 		Instance: module.Instance,
			// 	})
			// }
			r.channelLock.RUnlock()
			notifyHandler(resourceList)
		}
	}
}

// Placeholder for getLogs function
func getWasmLogs(taskid string) string {
	// Implement log retrieval if needed
	return ""
}

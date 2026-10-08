#include <cstdio>
#include <string>
#define __in
#define __out
#define _Analysis_noreturn_
#include <windows.h>
#include <initguid.h>
#include <dbgeng.h>
#undef __in
#undef __out

class DumpOutput : public IDebugOutputCallbacks {
public:
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID id, void** result) override {
        if (id == IID_IUnknown || id == IID_IDebugOutputCallbacks) {
            *result = this;
            return S_OK;
        }
        *result = nullptr;
        return E_NOINTERFACE;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return 1; }
    ULONG STDMETHODCALLTYPE Release() override { return 1; }
    HRESULT STDMETHODCALLTYPE Output(ULONG, PCSTR text) override {
        std::fputs(text, stdout);
        std::fflush(stdout);
        return S_OK;
    }
};

int main(int argc, char** argv) {
    if (argc != 4 && argc != 5) return 2;
    SetDllDirectoryA(argv[1]);
    const auto library = LoadLibraryExA((std::string(argv[1]) + "\\dbgeng.dll").c_str(), nullptr, LOAD_WITH_ALTERED_SEARCH_PATH);
    if (!library) return 3;
    const auto create = reinterpret_cast<HRESULT(WINAPI*)(REFIID, PVOID*)>(GetProcAddress(library, "DebugCreate"));
    IDebugClient* client = nullptr;
    IDebugControl* control = nullptr;
    IDebugSymbols* symbols = nullptr;
    auto result = create(IID_IDebugClient, reinterpret_cast<void**>(&client));
    if (FAILED(result)) return 4;
    DumpOutput output;
    client->SetOutputCallbacks(&output);
    client->QueryInterface(IID_IDebugControl, reinterpret_cast<void**>(&control));
    client->QueryInterface(IID_IDebugSymbols, reinterpret_cast<void**>(&symbols));
    symbols->SetSymbolPath(argv[3]);
    if (argc == 5) symbols->SetImagePath(argv[4]);
    result = client->OpenDumpFile(argv[2]);
    if (SUCCEEDED(result)) result = control->WaitForEvent(0, INFINITE);
    if (SUCCEEDED(result)) {
        for (const char* command : {".bugcheck", ".reload /f WinDivert64.sys", "kv", "lmvm WinDivert64"}) {
            result = control->Execute(DEBUG_OUTCTL_THIS_CLIENT, command, DEBUG_EXECUTE_DEFAULT);
            if (FAILED(result)) break;
        }
    }
    std::fprintf(stderr, "Debug result: 0x%08lx\n", static_cast<unsigned long>(result));
    client->EndSession(DEBUG_END_PASSIVE);
    symbols->Release();
    control->Release();
    client->Release();
    FreeLibrary(library);
    return FAILED(result) ? 1 : 0;
}

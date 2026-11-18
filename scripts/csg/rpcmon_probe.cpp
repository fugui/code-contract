// 只读探针：通过 DOPRA 官方 SDK（CROS RPC + CSG 服务发现）调用
// RPCMonitorService 的 5 个只读接口，把结果以 JSON 打印到 stdout。
//
// 这里只做查询：不 PUB / UNPUB，不申请端口，不影响现网业务。
// 由 cmd/rpcmonitor -mode native 调用并解析 JSON。
//
// 构建见 scripts/build-native-probe.sh（需在装有 SW SDK 的机器上编译）。
#include <cstdio>
#include <iostream>
#include <string>
#include <ctime>

#include "Platform_if.h"
#include "RPCMonitorServicePgStub.h"
#include "sw/rpc/gen/cplus/RPCMonitorServiceMsg.pb.h"
#include "sw/gen/cplus/BasicTypeMsg.pb.h"
#include "csg_client.h"
#include "csg_langbase.h"

extern "C" unsigned int CSG_SubServiceByAssignedPort(const char *svrName, CSG_SUB_ATTR_S *attr);
extern "C" void *CSG_WaitForServiceClient(const char *svrName, unsigned int timeout, unsigned int schId);

using namespace swp::rpc;
using namespace DDM::CrosCpp;

static const char *kSvcName = "RPCMonitorService-RPCMonitorService";

static std::string jsonEscape(const std::string &in)
{
    std::string out;
    out.reserve(in.size() + 8);
    for (size_t i = 0; i < in.size(); ++i) {
        unsigned char c = static_cast<unsigned char>(in[i]);
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (c < 0x20) {
                    char buf[8];
                    (void)snprintf(buf, sizeof(buf), "\\u%04x", c);
                    out += buf;
                } else {
                    out += static_cast<char>(c);
                }
        }
    }
    return out;
}

int main(int argc, char **argv)
{
    const char *csgAddr = (argc > 1) ? argv[1] : "ipv4:10.29.92.80:31806";

    HGXY_ERR_CODE rc = PlatformInit(false);
    if (rc != 0) {
        std::cerr << "PlatformInit failed: 0x" << std::hex << rc << std::dec << std::endl;
        return 1;
    }

    CSG_CLIENT_INIT_ATTR_S initAttr = CSG_CLIENT_INIT_ATTR_DEFAULT_VAL;
    unsigned int ci = CSG_ClientInit(csgAddr, &initAttr);
    // 0x62400002：客户端已初始化（本进程重复初始化时出现），不影响后续调用。
    if (ci != 0 && ci != 0x62400002U) {
        std::cerr << "CSG_ClientInit(" << csgAddr << ") failed: 0x" << std::hex << ci << std::dec << std::endl;
        return 1;
    }

    CSG_SUB_ATTR_S subAttr = CSG_SUB_ATTR_DEFAULT_VAL;
    subAttr.uiCommBuffSize = 1024 * 1024;
    subAttr.uiMsgMaxSize = 1024 * 1024;
    subAttr.ucLang = 1;
    subAttr.uiTimeOutS = 5;
    unsigned int sr = CSG_SubServiceByAssignedPort(kSvcName, &subAttr);
    if (sr != 0) {
        std::cerr << "CSG_SubServiceByAssignedPort failed: 0x" << std::hex << sr << std::dec << std::endl;
        return 1;
    }
    void *hClient = CSG_WaitForServiceClient(kSvcName, 10, 0);
    if (hClient == NULL) {
        std::cerr << "CSG_WaitForServiceClient timeout: service not published" << std::endl;
        return 1;
    }

    RPCMonitorServiceStub stub;
    VoidType in;

    UInt32Type serverNum;
    CrosStatusEx stNum = stub.GetRpcServerNumInfo(in, serverNum);
    if (!stNum.bRpcSuccess) {
        std::cerr << "GetRpcServerNumInfo failed: 0x" << std::hex << stNum.ullErrNo << std::dec << std::endl;
        return 1;
    }

    GetRpcServerDetailInfoResponse servers;
    CrosStatusEx stSvr = stub.GetRpcServerDetailInfo(in, servers);
    if (!stSvr.bRpcSuccess) {
        std::cerr << "GetRpcServerDetailInfo failed: 0x" << std::hex << stSvr.ullErrNo << std::dec << std::endl;
        return 1;
    }

    UInt32Type clientNum;
    CrosStatusEx stNum2 = stub.GetRpcClientNumInfo(in, clientNum);
    if (!stNum2.bRpcSuccess) {
        std::cerr << "GetRpcClientNumInfo failed: 0x" << std::hex << stNum2.ullErrNo << std::dec << std::endl;
        return 1;
    }

    GetRpcClientDetailInfoResponse clients;
    CrosStatusEx stCli = stub.GetRpcClientDetailInfo(in, clients);
    if (!stCli.bRpcSuccess) {
        std::cerr << "GetRpcClientDetailInfo failed: 0x" << std::hex << stCli.ullErrNo << std::dec << std::endl;
        return 1;
    }

    GetRpcPortInfoResponse ports;
    CrosStatusEx stPort = stub.GetRpcPortInfo(in, ports);
    if (!stPort.bRpcSuccess) {
        std::cerr << "GetRpcPortInfo failed: 0x" << std::hex << stPort.ullErrNo << std::dec << std::endl;
        return 1;
    }

    long long ts = servers.timestamp();
    if (ts == 0) {
        ts = static_cast<long long>(time(NULL));
    }

    std::cout << "{\n";
    std::cout << "  \"serverNum\": " << serverNum.value() << ",\n";
    std::cout << "  \"clientNum\": " << clientNum.value() << ",\n";
    std::cout << "  \"timestamp\": " << ts << ",\n";

    std::cout << "  \"portInfo\": [";
    for (int i = 0; i < ports.infos_size(); ++i) {
        const RpcPortInfo &p = ports.infos(i);
        std::cout << (i == 0 ? "\n" : ",\n")
                  << "    {\"ipAddr\": \"" << jsonEscape(p.ipaddr()) << "\", \"uiPortUsed\": " << p.uiportused()
                  << ", \"uiPortTotal\": " << p.uiporttotal() << "}";
    }
    std::cout << (ports.infos_size() > 0 ? "\n  ],\n" : "],\n");

    std::cout << "  \"servers\": [";
    for (int i = 0; i < servers.infos_size(); ++i) {
        const RpcServerDetailInfo &s = servers.infos(i);
        std::cout << (i == 0 ? "\n" : ",\n")
                  << "    {\"procName\": \"" << jsonEscape(s.procname()) << "\", \"pid\": " << s.pid()
                  << ", \"serverName\": \"" << jsonEscape(s.servername()) << "\", \"serverAddr\": \""
                  << jsonEscape(s.serveraddr()) << "\"}";
    }
    std::cout << (servers.infos_size() > 0 ? "\n  ],\n" : "],\n");

    std::cout << "  \"clients\": [";
    for (int i = 0; i < clients.infos_size(); ++i) {
        const RpcClientDetailInfo &c = clients.infos(i);
        std::cout << (i == 0 ? "\n" : ",\n")
                  << "    {\"procName\": \"" << jsonEscape(c.procname()) << "\", \"pid\": " << c.pid()
                  << ", \"clientAddr\": \"" << jsonEscape(c.clientaddr()) << "\", \"serverName\": \""
                  << jsonEscape(c.servername()) << "\"}";
    }
    std::cout << (clients.infos_size() > 0 ? "\n  ]\n" : "]\n");
    std::cout << "}\n";

    stub.CrosDisconnect();
    return 0;
}

import React from "react";
import { createRoot } from "react-dom/client";
import { ConfigProvider, App as AntApp } from "antd";
import zhCN from "antd/locale/zh_CN";
import { Secretary } from "./secretary";
import "./style.css";
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: "#246b60",
          borderRadius: 10,
          fontFamily: '"Inter","PingFang SC","Microsoft YaHei",sans-serif',
          colorBgLayout: "#f5f6f3",
        },
      }}
    >
      <AntApp>
        <Secretary />
      </AntApp>
    </ConfigProvider>
  </React.StrictMode>,
);

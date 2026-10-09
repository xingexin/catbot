import React from "react";
import { createRoot } from "react-dom/client";
import { ConfigProvider, App as AntApp } from "antd";
import zhCN from "antd/locale/zh_CN";
import { Catbot } from "./catbot";
import "./style.css";
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: "#282529",
          colorInfo: "#876275",
          colorLink: "#945c76",
          colorLinkHover: "#6f4157",
          colorText: "#242126",
          colorTextSecondary: "#827985",
          colorBorder: "#e5dfe4",
          colorBorderSecondary: "#eee9ec",
          colorBgLayout: "#fbfafb",
          colorBgContainer: "#ffffff",
          colorFillAlter: "#faf7f9",
          borderRadius: 10,
          fontFamily:
            '"Inter",-apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif',
          controlHeight: 36,
        },
        components: {
          Menu: {
            itemSelectedBg: "#faecf2",
            itemSelectedColor: "#734e63",
            itemHoverBg: "#f6f3f5",
            itemColor: "#716874",
            itemHeight: 43,
            iconSize: 16,
          },
          Button: { primaryShadow: "none", defaultShadow: "none" },
          Input: {
            activeBorderColor: "#cdaabd",
            hoverBorderColor: "#cdaabd",
            activeShadow: "0 0 0 3px #f6e7ee",
          },
          Select: {
            optionSelectedBg: "#faecf2",
            activeBorderColor: "#cdaabd",
            hoverBorderColor: "#cdaabd",
            activeOutlineColor: "#f6e7ee",
          },
          Checkbox: {
            colorPrimary: "#945c76",
            colorPrimaryHover: "#78485f",
          },
          Table: {
            headerBg: "#faf8fa",
            headerColor: "#746975",
            borderColor: "#eee9ec",
            rowHoverBg: "#fdf8fb",
            rowSelectedBg: "#faecf2",
            rowSelectedHoverBg: "#f5dce7",
          },
          Card: { headerFontSize: 15 },
          Modal: { titleFontSize: 19 },
          Tabs: {
            inkBarColor: "#d9aec2",
            itemSelectedColor: "#734e63",
            itemHoverColor: "#945c76",
          },
        },
      }}
    >
      <AntApp>
        <Catbot />
      </AntApp>
    </ConfigProvider>
  </React.StrictMode>,
);

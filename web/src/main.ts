import { mount } from "svelte";
import "./app.css";
import App from "./App.svelte";
import Admin from "./Admin.svelte";

const path = window.location.pathname;
const isAdminPath = path === "/admin" || path.startsWith("/admin/");
const target = document.getElementById("app")!;

const app = isAdminPath
  ? mount(Admin, { target })
  : mount(App, { target });

export default app

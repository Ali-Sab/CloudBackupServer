package com.alison.CloudBackupServer;

import com.google.gson.JsonObject;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class DataController {

    @GetMapping("/")
    public String index() {
        return "My Root Page";
    }

    @GetMapping("/home")
    public String home() {
        JsonObject obj = new JsonObject();
        obj.addProperty("Hello", "WORLD");
        return obj.toString();
    }
}
